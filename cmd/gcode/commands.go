package main

// Live commands wired in M4 batch 3: run / resume / doctor. The agent loop,
// providers, tools, and session persistence are the M1–M4 library work; this
// file only assembles and renders.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/agent"
	"github.com/ChaosJohn/gcode/internal/appcore"
	"github.com/ChaosJohn/gcode/internal/mcp"
	"github.com/ChaosJohn/gcode/internal/types"
)

// cliHooks renders tool activity and streams model text to the terminal.
func cliHooks(stdout, stderr io.Writer) agent.ToolHooks {
	return agent.ToolHooks{
		OnEvent: func(event types.StreamEvent) error {
			if delta, ok := event.(types.TextDelta); ok {
				fmt.Fprint(stdout, delta.Text)
			}
			return nil
		},
		OnToolStart: func(call types.ToolCall) {
			fmt.Fprintf(stdout, "\n[%s] ", call.Name)
		},
		OnToolEnd: func(call types.ToolCall, summary string, isErr bool) {
			marker := "ok"
			if isErr {
				marker = "error"
			}
			fmt.Fprintf(stdout, "(%s) %s\n", marker, summary)
		},
	}
}

// runRun implements `gcode run <prompt>`: one headless agent run.
func runRun(args []string, stdout, stderr io.Writer) int {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" {
		fmt.Fprintln(stderr, "usage: gcode run <prompt>")
		return exitUsage
	}
	sessionID, _, err := appcore.RunHeadless(prompt, cliHooks(stdout, stderr))
	if err != nil {
		fmt.Fprintf(stderr, "\ngcode run: %v\n", err)
		return exitInternal
	}
	fmt.Fprintf(stdout, "\n\n(session %s — resume with `gcode resume %s`)\n", sessionID, sessionID)
	return exitOK
}

// runResume implements `gcode resume [session] [prompt...]`: with no
// session it lists candidates; with one it restores the transcript and
// continues with the optional prompt.
func runResume(args []string, stdout, stderr io.Writer) int {
	home := appcore.GcodeHome()
	if len(args) == 0 {
		sessions, err := sessionstoreList(home)
		if err != nil {
			fmt.Fprintf(stderr, "gcode resume: %v\n", err)
			return exitInternal
		}
		if len(sessions) == 0 {
			fmt.Fprintf(stdout, "No sessions in %s yet. Start one with `gcode run <prompt>`.\n", home)
			return exitOK
		}
		fmt.Fprintf(stdout, "Sessions (newest first):\n")
		shown := len(sessions)
		if shown > 10 {
			shown = 10
		}
		for _, session := range sessions[:shown] {
			title := session.ID
			if session.Title != nil && *session.Title != "" {
				title = *session.Title
			}
			fmt.Fprintf(stdout, "  %-24s %s (%d messages)\n", session.ID, title, session.Messages)
		}
		fmt.Fprintf(stdout, "\nContinue with `gcode resume <session-id> [prompt]`.\n")
		return exitOK
	}

	sessionID := args[0]
	store, err := appcore.LoadSessionStore(sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "gcode resume: %v\n", err)
		return exitUsage
	}
	loop, _, err := appcore.Assemble(appcore.NewSessionID(), cliHooks(stdout, stderr))
	if err != nil {
		fmt.Fprintf(stderr, "gcode resume: %v\n", err)
		return exitInternal
	}
	// Rebuild the transcript from the stored session.
	for _, stored := range store.Session.Messages {
		loop.AddMessage(stored.Role, stored.ToMessage().Content)
	}
	// Fork: the new turn saves under a fresh id, leaving the source intact.
	loop.WirePersistence(nil)
	prompt := strings.TrimSpace(strings.Join(args[1:], " "))
	if prompt == "" {
		fmt.Fprintf(stdout, "Restored %s (%d messages). Pass a prompt to continue.\n",
			sessionID, len(store.Session.Messages))
		return exitOK
	}
	// Re-wire persistence to a fresh session carrying the restored history.
	fresh := appcore.NewSessionID()
	loop.SessionID = fresh
	if err := loop.SetPersistenceSession(fresh); err != nil {
		fmt.Fprintf(stderr, "gcode resume: %v\n", err)
		return exitInternal
	}
	if _, err := loop.RunOnce(prompt); err != nil {
		fmt.Fprintf(stderr, "\ngcode resume: %v\n", err)
		return exitInternal
	}
	fmt.Fprintf(stdout, "\n\n(continued as session %s)\n", fresh)
	return exitOK
}

// runDoctor implements `gcode doctor`: static environment diagnostics, no
// network calls.
// runMcpLogin performs the interactive MCP OAuth flow: dynamic client
// registration, a loopback redirect listener, and PKCE code exchange.
func runMcpLogin(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: gcode mcp-login <server-url>")
		return exitUsage
	}
	serverURL := strings.TrimSpace(args[0])
	metadata, err := mcp.DiscoverAuthorizationServer(serverURL)
	if err != nil {
		fmt.Fprintf(stderr, "gcode mcp-login: %v\n", err)
		return exitInternal
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(stderr, "gcode mcp-login: loopback listener: %v\n", err)
		return exitInternal
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	clientID, err := mcp.RegisterClient(metadata, redirectURI)
	if err != nil {
		fmt.Fprintf(stderr, "gcode mcp-login: %v\n", err)
		return exitInternal
	}
	verifier, err := mcp.PKCEVerifier()
	if err != nil {
		return exitInternal
	}
	state := fmt.Sprintf("%d", time.Now().UnixNano())
	authURL, err := mcp.BuildAuthorizationURL(metadata, clientID, redirectURI, state, verifier)
	if err != nil {
		return exitInternal
	}
	fmt.Fprintf(stdout, "Open this URL to authorize MCP access:\n%s\n\nWaiting for the redirect...\n", authURL)

	type loginCallback struct {
		code    string
		state   string
		errText string
	}
	callbacks := make(chan loginCallback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		item := loginCallback{code: query.Get("code"), state: query.Get("state"),
			errText: query.Get("error")}
		fmt.Fprintln(w, "Authorization received. You can close this tab.")
		select {
		case callbacks <- item:
		default:
		}
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	select {
	case item := <-callbacks:
		if item.errText != "" {
			fmt.Fprintf(stderr, "gcode mcp-login: authorization failed: %s\n", item.errText)
			return exitInternal
		}
		if item.state != state {
			fmt.Fprintln(stderr, "gcode mcp-login: state mismatch")
			return exitInternal
		}
		token, err := mcp.ExchangeAuthorizationCode(serverURL, metadata, clientID,
			redirectURI, item.code, verifier)
		if err != nil {
			fmt.Fprintf(stderr, "gcode mcp-login: %v\n", err)
			return exitInternal
		}
		fmt.Fprintf(stdout, "Authorized %s (token expires %s)\n", token.ServerURL,
			token.ExpiresAt.Format(time.RFC3339))
		_ = shutdownServer(server, 500*time.Millisecond)
		return exitOK
	case <-time.After(5 * time.Minute):
		fmt.Fprintln(stderr, "gcode mcp-login: timed out waiting for authorization")
		_ = shutdownServer(server, 500*time.Millisecond)
		return exitInternal
	}
}

func shutdownServer(server *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return server.Shutdown(ctx)
}

func runDoctor(_ []string, stdout, stderr io.Writer) int {
	failed := false
	check := func(name, detail string, ok bool) {
		marker := "✓"
		if !ok {
			marker = "✗"
			failed = true
		}
		fmt.Fprintf(stdout, "  %s %s: %s\n", marker, name, detail)
	}
	fmt.Fprintln(stdout, "gcode doctor")

	home := appcore.GcodeHome()
	check("gcode home", home, true)
	cfg, err := appcore.LoadConfig()
	check("config", detailOr(err, fmt.Sprintf("%s/config.toml loaded", home)), err == nil)

	providerName := "claude"
	if cfg != nil && cfg.Provider.DefaultProvider != nil && *cfg.Provider.DefaultProvider != "" {
		providerName = strings.ToLower(*cfg.Provider.DefaultProvider)
	}
	if env := strings.TrimSpace(os.Getenv("GCODE_PROVIDER")); env != "" {
		providerName = strings.ToLower(env)
	}
	model := "(provider default)"
	if cfg != nil && cfg.Provider.DefaultModel != nil && *cfg.Provider.DefaultModel != "" {
		model = *cfg.Provider.DefaultModel
	}
	if env := strings.TrimSpace(os.Getenv("GCODE_MODEL")); env != "" {
		model = env
	}
	fmt.Fprintf(stdout, "  • provider: %s (model %s)\n", providerName, model)

	switch providerName {
	case "claude", "anthropic":
		_, keyErr := hasAnthropicCredential()
		check("claude credentials", detailOr(keyErr, "ANTHROPIC_API_KEY or OAuth store found"), keyErr == nil)
	case "codex", "openai":
		_, keyErr := hasCodexCredential()
		check("codex credentials", detailOr(keyErr, "CODEX_API_KEY or ~/.codex/auth.json found"), keyErr == nil)
	case "gemini":
		check("gemini credentials", "Code Assist OAuth handshake runs on first call", true)
	}

	registry := appcore.BuildRegistry()
	check("tools", fmt.Sprintf("%d registered: %s", len(registry.Names()),
		strings.Join(registry.Names(), ", ")), len(registry.Names()) > 0)

	if servers := mcp.LoadAuthStore(); len(servers) > 0 {
		names := make([]string, 0, len(servers))
		for url := range servers {
			names = append(names, url)
		}
		sort.Strings(names)
		fmt.Fprintf(stdout, "  • mcp oauth tokens: %d stored (%s)\n",
			len(names), strings.Join(names, ", "))
	}
	if cfg != nil && len(cfg.Providers) > 0 {
		names := make([]string, 0, len(cfg.Providers))
		for name := range cfg.Providers {
			names = append(names, name)
		}
		fmt.Fprintf(stdout, "  • named providers: %s (select with GCODE_PROVIDER=<name>)\n",
			strings.Join(names, ", "))
	}

	sessions, listErr := sessionstoreList(home)
	check("sessions", detailOr(listErr, fmt.Sprintf("%d stored in %s/sessions",
		len(sessions), home)), listErr == nil)

	if failed {
		fmt.Fprintln(stderr, "doctor: problems found (see ✗ above)")
		return exitInternal
	}
	return exitOK
}

func detailOr(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

// hasAnthropicCredential reports credential availability without network.
func hasAnthropicCredential() (bool, error) {
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		return true, nil
	}
	if creds, err := appcore.LoadClaudeCredentials(); err == nil {
		return creds != "", nil
	} else {
		return false, err
	}
}

// hasCodexCredential reports codex credential availability without network.
func hasCodexCredential() (bool, error) {
	if strings.TrimSpace(os.Getenv("CODEX_API_KEY")) != "" {
		return true, nil
	}
	authPath := os.Getenv("CODEX_HOME")
	if authPath == "" {
		home, _ := os.UserHomeDir()
		authPath = filepath.Join(home, ".codex", "auth.json")
	}
	if _, err := os.Stat(authPath); err != nil {
		return false, fmt.Errorf("no codex credentials (set CODEX_API_KEY or %s)", authPath)
	}
	return true, nil
}

func sessionstoreList(home string) ([]sessionSummary, error) {
	sessions, err := appcore.ListSessions(home)
	if err != nil {
		return nil, err
	}
	out := make([]sessionSummary, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, sessionSummary{
			ID: session.ID, Title: session.CustomTitle,
			Messages: len(session.Messages),
		})
	}
	return out, nil
}

type sessionSummary struct {
	ID       string
	Title    *string
	Messages int
}
