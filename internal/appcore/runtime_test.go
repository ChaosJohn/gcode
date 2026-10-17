// Named-provider assembly: config table selection, env key fallback, wire
// mode mapping, and error surfaces.
package appcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/provider"
)

func TestBuildNamedOpenAICompatible(t *testing.T) {
	t.Setenv("GCODE_PROVIDER", "deepseek")
	t.Setenv("OPENAI_API_KEY", "")
	named := config.NamedProviderConfig{
		ProviderType: config.NamedProviderOpenAICompatible,
		BaseURL:      "https://api.deepseek.test/v1",
		APIKeyEnv:    strPtrConfig("DEEPSEEK_KEY"),
		API:          strPtrConfig("chat-completions"),
		DefaultModel: strPtrConfig("deepseek-chat"),
	}
	// Missing key is a clean error.
	if _, err := BuildProvider(configWithNamed("deepseek", named), "s"); err == nil ||
		!strings.Contains(err.Error(), "no api key") {
		t.Fatalf("missing key: %v", err)
	}
	t.Setenv("DEEPSEEK_KEY", "sk-test")
	prov, err := BuildProvider(configWithNamed("deepseek", named), "s")
	if err != nil {
		t.Fatal(err)
	}
	if prov.Name() != "codex" {
		t.Fatalf("adapter name: %s", prov.Name())
	}
	if prov.Model() != "deepseek-chat" {
		t.Fatalf("model: %s", prov.Model())
	}
}

// The GCODE_PROVIDER selector matches a providers table key without
// provider_type (defaults to openai-compatible).
func TestProviderSelectorMatchesNamedTable(t *testing.T) {
	t.Setenv("GCODE_PROVIDER", "mygateway")
	t.Setenv("OPENAI_API_KEY", "sk-gw")
	t.Setenv("GCODE_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Providers["mygateway"] = config.NamedProviderConfig{
		BaseURL: "https://gw.test/v1",
	}
	prov, err := BuildProvider(cfg, "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prov.(*provider.OpenAI); !ok {
		t.Fatalf("adapter type: %T", prov)
	}
}

// A claude-compatible named table falls through to the Claude chain.
func TestNamedAnthropicCompatible(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	t.Setenv("JCODE_HOME", filepath.Join(t.TempDir(), "absent"))
	named := config.NamedProviderConfig{ProviderType: config.NamedProviderAnthropicCompatible}
	prov, err := BuildProvider(configWithNamed("myclaude", named), "s")
	// Type dispatch: anthropic-compatible routes to the claude adapter
	// (credentials resolve lazily at request time).
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if prov.Name() != "claude" {
		t.Fatalf("adapter: %s", prov.Name())
	}
}

func configWithNamed(name string, named config.NamedProviderConfig) *config.Config {
	cfg := config.Default()
	cfg.Providers[name] = named
	return cfg
}

func strPtrConfig(s string) *string { return &s }

var _ = os.Environ
