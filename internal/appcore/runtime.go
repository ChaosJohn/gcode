// Package appcore assembles the runnable application: config resolution,
// provider construction from credentials, the tool registry, and the agent
// wired to session persistence. The CLI commands are thin wrappers over this.
package appcore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/agent"
	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/provider"
	"github.com/ChaosJohn/gcode/internal/provider/anthropic"
	"github.com/ChaosJohn/gcode/internal/provider/gemini"
	"github.com/ChaosJohn/gcode/internal/provider/openai"
	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/toolcore"
	"github.com/ChaosJohn/gcode/internal/tools"
)

// GcodeHome resolves the gcode home (GCODE_HOME → ~/.gcode).
func GcodeHome() string {
	if home := os.Getenv("GCODE_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".gcode"
	}
	return filepath.Join(home, ".gcode")
}

// LoadConfig reads config.toml over defaults (missing file = defaults).
func LoadConfig() (*config.Config, error) {
	cfg := config.Default()
	path := filepath.Join(GcodeHome(), "config.toml")
	if _, err := os.Stat(path); err != nil {
		return cfg, nil
	}
	loaded, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

// cliSystemPrompt is the compact base prompt for the headless CLI (the full
// jcode prompt bundle ships with the TUI milestone).
const cliSystemPrompt = `You are gcode, a coding agent in the user's repository. Work directly with the tools available to you. Prefer the read/edit/write tools over shell redirection for file changes, and agentgrep over grep -r for repository searches. Be concise.`

// BuildProvider constructs the configured provider from available
// credentials. Supported selectors: claude (OAuth or ANTHROPIC_API_KEY),
// codex (~/.codex/auth.json OAuth or CODEX_API_KEY), gemini (Code Assist
// OAuth handshake on first call).
func BuildProvider(cfg *config.Config, sessionID string) (provider.Provider, error) {
	selector := ""
	if cfg.Provider.DefaultProvider != nil {
		selector = strings.ToLower(strings.TrimSpace(*cfg.Provider.DefaultProvider))
	}
	if env := strings.TrimSpace(os.Getenv("GCODE_PROVIDER")); env != "" {
		selector = strings.ToLower(env)
	}
	if selector == "" {
		selector = "claude"
	}
	model := ""
	if cfg.Provider.DefaultModel != nil {
		model = strings.TrimSpace(*cfg.Provider.DefaultModel)
	}
	if env := strings.TrimSpace(os.Getenv("GCODE_MODEL")); env != "" {
		model = env
	}
	// A named [providers.<selector>] table wins when present (jcode
	// NamedProviderConfig): base_url + api_key(_env) + optional wire api.
	if named, ok := cfg.Providers[selector]; ok {
		return buildNamedProvider(selector, named, model)
	}

	switch selector {
	case "claude", "anthropic":
		return buildClaude(model, sessionID)
	case "codex", "openai":
		return buildCodex(model)
	case "gemini":
		return provider.NewGemini(model, provider.GeminiOptions{Client: gemini.NewClient()}), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (supported: claude, codex, gemini)", selector)
	}
}

// buildNamedProvider assembles a provider from a [providers.NAME] table.
// Supported: openai-compatible (base_url + api_key_env/api_key, wire
// "responses"|"chat-completions" — default chat-completions for
// openai-compatible), plus passthrough types claude/codex/gemini.
func buildNamedProvider(name string, named config.NamedProviderConfig, model string) (provider.Provider, error) {
	apiKey := ""
	if named.APIKey != nil {
		apiKey = strings.TrimSpace(*named.APIKey)
	}
	if named.APIKeyEnv != nil {
		if env := strings.TrimSpace(os.Getenv(*named.APIKeyEnv)); env != "" {
			apiKey = env
		}
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	wire := "chat-completions"
	if named.API != nil && strings.TrimSpace(*named.API) != "" {
		wire = strings.ToLower(strings.TrimSpace(*named.API))
	}
	if named.DefaultModel != nil && model == "" {
		model = *named.DefaultModel
	}
	switch strings.ToLower(string(named.ProviderType)) {
	case "", "openai-compatible":
		if named.BaseURL == "" {
			return nil, fmt.Errorf("provider %q: base_url is required for openai-compatible", name)
		}
		if apiKey == "" {
			return nil, fmt.Errorf("provider %q: no api key (set api_key, api_key_env, or OPENAI_API_KEY)", name)
		}
		return provider.NewOpenAI(model, provider.OpenAIOptions{
			WireAPI: wire, BaseURL: named.BaseURL, APIKey: apiKey,
			Credentials: func() (*openai.CodexCredentials, error) {
				return &openai.CodexCredentials{AccessToken: apiKey}, nil
			},
		}), nil
	case "anthropic-compatible":
		return buildClaude(model, "named-"+name)
	case "gemini":
		return provider.NewGemini(model, provider.GeminiOptions{Client: gemini.NewClient()}), nil
	default:
		return nil, fmt.Errorf("provider %q: unsupported provider_type %q", name, named.ProviderType)
	}
}

func buildClaude(model, sessionID string) (provider.Provider, error) {
	// API key wins for headless use; otherwise the OAuth chain (native
	// Claude Code credentials, env credentials, jcode store with refresh).
	if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
		return provider.NewAnthropic(model, provider.AnthropicOptions{
			CompleteConfig: anthropic.CompleteConfig{
				IsOAuth:      false,
				ResolveToken: func(bool) (string, error) { return key, nil },
			},
			SessionID: sessionID,
		}), nil
	}
	creds, credErr := anthropic.LoadCredentials()
	return provider.NewAnthropic(model, provider.AnthropicOptions{
		CompleteConfig: anthropic.CompleteConfig{
			IsOAuth: true,
			ResolveToken: func(force bool) (string, error) {
				if force || credErr != nil || creds.Expired() {
					fresh, err := anthropic.LoadCredentials()
					if err != nil {
						return "", err
					}
					creds = fresh
					credErr = nil
				}
				if creds.Expired() && creds.RefreshToken != "" {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					refreshed, err := anthropic.RefreshClaudeTokens(ctx, creds.RefreshToken)
					if err != nil {
						return "", err
					}
					creds.AccessToken = refreshed.AccessToken
					creds.RefreshToken = refreshed.RefreshToken
					creds.ExpiresAtMillis = refreshed.ExpiresAt
				}
				return creds.AccessToken, nil
			},
			LoadCredentials: func() (anthropic.ClaudeCredentials, error) {
				return anthropic.LoadCredentials()
			},
		},
		SessionID: sessionID,
	}), nil
}

func buildCodex(model string) (provider.Provider, error) {
	if key := strings.TrimSpace(os.Getenv("CODEX_API_KEY")); key != "" {
		creds := &openai.CodexCredentials{AccessToken: key}
		return provider.NewOpenAI(model, provider.OpenAIOptions{
			Credentials: func() (*openai.CodexCredentials, error) { return creds, nil },
		}), nil
	}
	// ChatGPT OAuth from the Codex CLI store.
	authPath := os.Getenv("CODEX_HOME")
	if authPath == "" {
		home, _ := os.UserHomeDir()
		authPath = filepath.Join(home, ".codex", "auth.json")
	}
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("no codex credentials (set CODEX_API_KEY or %s)", authPath)
	}
	var creds openai.CodexCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("unreadable codex auth at %s: %w", authPath, err)
	}
	return provider.NewOpenAI(model, provider.OpenAIOptions{
		Credentials: func() (*openai.CodexCredentials, error) { return &creds, nil },
	}), nil
}

// BuildRegistry registers every implemented tool.
func BuildRegistry() *agent.Registry {
	registry := agent.NewRegistry()
	for _, tool := range []toolcore.Tool{
		tools.NewReadTool(),
		tools.NewWriteTool(),
		tools.NewEditTool(),
		tools.NewLsTool(),
		tools.NewAgentGrepTool(),
		tools.NewBashTool(),
		tools.NewWebFetchTool(),
		tools.NewWebSearchTool(),
		tools.NewSessionSearchTool(),
		tools.NewMcpSearchTool(),
		tools.NewMcpCallTool(),
		tools.NewMcpTool(),
		tools.NewReplaceTool(),
		tools.NewApplyPatchTool(),
		tools.NewBatchTool(),
	} {
		registry.Register(tool)
	}
	tools.SetBatchRegistry(registry)
	return registry
}

// NewSessionID derives a fresh session id (time-ordered, jcode-shaped).
func NewSessionID() string {
	return fmt.Sprintf("s-%d", time.Now().UnixNano())
}

// Assemble builds a runnable agent (provider + registry + persistence).
func Assemble(sessionID string, hooks agent.ToolHooks) (*agent.Agent, *sessionstore.Store, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	prov, err := BuildProvider(cfg, sessionID)
	if err != nil {
		return nil, nil, err
	}
	home := GcodeHome()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o755); err != nil {
		return nil, nil, err
	}
	store := sessionstore.NewStore(sessionstore.Session{
		ID: sessionID, Status: "active", CreatedAt: timePtr(time.Now()),
	}, home)
	agentLoop := agent.NewAgent(prov, BuildRegistry())
	agentLoop.System = cliSystemPrompt
	agentLoop.WorkingDir, _ = os.Getwd()
	agentLoop.Hooks = hooks
	agentLoop.WirePersistence(store)
	return agentLoop, store, nil
}

// RunHeadless executes one prompt and streams output to stdout hooks.
func RunHeadless(prompt string, hooks agent.ToolHooks) (sessionID string, final string, err error) {
	sessionID = NewSessionID()
	loop, _, err := Assemble(sessionID, hooks)
	if err != nil {
		return "", "", err
	}
	final, err = loop.RunOnce(prompt)
	return sessionID, final, err
}

// LoadClaudeCredentials reports the OAuth store without network calls
// ("" = none found).
func LoadClaudeCredentials() (string, error) {
	creds, err := anthropic.LoadCredentials()
	if err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}

// ListSessions enumerates stored sessions, newest first.
func ListSessions(home string) ([]sessionstore.Session, error) {
	return sessionstore.ListSessions(home)
}

// LoadSessionStore opens an existing session for resume.
func LoadSessionStore(sessionID string) (*sessionstore.Store, error) {
	return sessionstore.Open(GcodeHome(), sessionID)
}

func timePtr(t time.Time) *time.Time { return &t }
