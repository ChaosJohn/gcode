package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultTemplateParses(t *testing.T) {
	raw, err := os.ReadFile("testdata/jcode/default_config.toml")
	if err != nil {
		t.Fatal(err)
	}
	effort := DefaultKeybindings("macos")
	text := strings.ReplaceAll(string(raw), "@EFFORT_INCREASE@", effort.EffortIncrease)
	text = strings.ReplaceAll(text, "@EFFORT_DECREASE@", effort.EffortDecrease)
	cfg, err := LoadString(text)
	if err != nil {
		t.Fatalf("official template must parse: %v", err)
	}
	// The template intentionally ships simpler scroll bindings than the
	// runtime table; the platform-substituted effort keys must match, and
	// every other templated section must equal our defaults.
	if cfg.Keybindings.EffortIncrease != effort.EffortIncrease || cfg.Keybindings.EffortDecrease != effort.EffortDecrease {
		t.Errorf("template effort keys must resolve per platform: %q / %q",
			cfg.Keybindings.EffortIncrease, cfg.Keybindings.EffortDecrease)
	}
	def := Default()
	if !reflect.DeepEqual(cfg.Display, def.Display) {
		t.Errorf("template display diverges:\n %+v\n %+v", cfg.Display, def.Display)
	}
	if cfg.Features != def.Features {
		t.Errorf("template features diverge:\n %+v\n %+v", cfg.Features, def.Features)
	}
	if !reflect.DeepEqual(cfg.WebSearch, def.WebSearch) {
		t.Errorf("template websearch diverge:\n %+v\n %+v", cfg.WebSearch, def.WebSearch)
	}
	if !reflect.DeepEqual(cfg.Provider, def.Provider) {
		t.Errorf("template provider diverge:\n %+v\n %+v", cfg.Provider, def.Provider)
	}
	if cfg.Compaction != def.Compaction {
		t.Errorf("template compaction diverge:\n %+v\n %+v", cfg.Compaction, def.Compaction)
	}
}

func TestDefaultsCoverTemplateSections(t *testing.T) {
	raw, err := os.ReadFile("testdata/jcode/default_config.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.Contains(line, ".") {
			section := strings.Trim(line, "[]")
			if !strings.HasPrefix(section, "#") && section != "" {
				if _, ok := knownTopLevelTables()[section]; !ok {
					t.Errorf("template section [%s] missing from Config", section)
				}
			}
		}
	}
}

func TestRoundTripSemanticEquality(t *testing.T) {
	cfg := Default()
	cfg.Display.SetReasoningDisplay(ReasoningCurrent)
	cfg.Provider.DefaultModel = strPtr("claude-opus-4-8")
	cfg.Providers["custom"] = NamedProviderConfig{
		ProviderType: NamedProviderAnthropicCompatible, BaseURL: "https://relay.example/v1",
		Auth: NamedAuthHeader, Models: []NamedProviderModelConfig{{ID: "m1", ContextWindow: uintPtr(200_000)}},
		ExtraBody: map[string]any{"thinking": map[string]any{"type": "enabled"}},
	}
	cfg.Extra = map[string]any{"future_tool": map[string]any{"enabled": true}}

	text, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := LoadString(string(text))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if back.Provider.DefaultModel == nil || *back.Provider.DefaultModel != "claude-opus-4-8" {
		t.Errorf("default_model lost: %v", back.Provider.DefaultModel)
	}
	if back.Display.ReasoningDisplay() != ReasoningCurrent {
		t.Errorf("reasoning display lost: %q", back.Display.ReasoningDisplay())
	}
	got := back.Providers["custom"]
	if got.ProviderType != NamedProviderAnthropicCompatible || got.Auth != NamedAuthHeader || len(got.Models) != 1 || got.Models[0].ContextWindow == nil || *got.Models[0].ContextWindow != 200_000 {
		t.Errorf("named provider lost: %+v", got)
	}
	if back.Extra["future_tool"] == nil {
		t.Error("extra table lost")
	}
}

func TestLenientEnums(t *testing.T) {
	cfg, err := LoadString(`
[features]
update_channel = "manual"

[server]
wake_mode = "bogus"

[display]
diff_mode = "inline"
latex_rendering = "raw"

[agents]
swarm_spawn_mode = "detached"

[tools]
mcp_tools = "eager"

[providers.relay]
provider_type = "OpenAI-Compatible"
auth = "HEADER"
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Features.UpdateChannel != ChannelStable {
		t.Errorf("unknown channel must fall back to stable, got %q", cfg.Features.UpdateChannel)
	}
	if cfg.Server.WakeMode != WakeInternal {
		t.Errorf("unknown wake mode must fall back to internal, got %q", cfg.Server.WakeMode)
	}
	if cfg.Display.LatexRendering != LatexNone {
		t.Errorf("alias raw must map to none, got %q", cfg.Display.LatexRendering)
	}
	if cfg.Agents.SwarmSpawnMode != SwarmSpawnInline {
		t.Errorf("unknown spawn mode must fall back to inline, got %q", cfg.Agents.SwarmSpawnMode)
	}
	if cfg.Tools.McpTools != McpToolsEager {
		t.Errorf("eager must parse, got %q", cfg.Tools.McpTools)
	}
	if cfg.Providers["relay"].ProviderType != NamedProviderOpenAICompatible {
		t.Errorf("kebab-case type must parse case-insensitively")
	}
	if cfg.Providers["relay"].Auth != NamedAuthHeader {
		t.Errorf("header auth must parse case-insensitively")
	}
}

func TestUnknownTablesLandInExtra(t *testing.T) {
	cfg, err := LoadString(`
[display]
emoji = false

[desktop]
voice = true

[fancy_new_section]
knob = 3
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Display.Emoji {
		t.Error("display.emoji must still parse")
	}
	if cfg.Extra["fancy_new_section"] == nil {
		t.Errorf("unknown table lost: %v", cfg.Extra)
	}
	if voice, ok := cfg.Desktop["voice"]; !ok || voice != true {
		t.Errorf("desktop passthrough lost: %v", cfg.Desktop)
	}
}

func TestAliasNormalization(t *testing.T) {
	cfg, err := LoadString(`
[providers.relay]
extra-body = { temperature = 0.7 }

[[providers.relay.models]]
id = "m1"
reasoning-effort = "high"
`)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["relay"]
	if p.ExtraBody == nil {
		t.Fatal("extra-body alias not normalized")
	}
	if _, ok := p.ExtraBody["temperature"]; !ok {
		t.Errorf("extra body payload wrong: %v", p.ExtraBody)
	}
	if len(p.Models) != 1 || p.Models[0].ReasoningEffort == nil || *p.Models[0].ReasoningEffort != "high" {
		t.Errorf("reasoning-effort alias not normalized: %+v", p.Models)
	}
}

func TestKeybindingPlatformSplit(t *testing.T) {
	mac := DefaultKeybindings("macos")
	other := DefaultKeybindings("other")
	if mac.EffortIncrease != "cmd+right" || other.EffortIncrease != "alt+right" {
		t.Errorf("effort_increase split wrong: %q / %q", mac.EffortIncrease, other.EffortIncrease)
	}
	if mac.OpenResume != "cmd+b" || other.OpenResume != "alt+r" {
		t.Errorf("open_resume split wrong: %q / %q", mac.OpenResume, other.OpenResume)
	}
	if mac.NewTerminal != "cmd+shift+;" || other.NewTerminal != "alt+shift+;" {
		t.Errorf("new_terminal split wrong: %q / %q", mac.NewTerminal, other.NewTerminal)
	}
	if mac.ScrollUpFallback != "" || other.ScrollUpFallback != "" {
		t.Error("scroll fallbacks stay unbound")
	}
	if mac.SessionPickerEnter != ResumeCurrentTerminal {
		t.Errorf("session_picker_enter default: %q", mac.SessionPickerEnter)
	}
}

func TestSaveWritesAllDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Compaction != cfg.Compaction || reloaded.Safety != cfg.Safety || reloaded.Agents != cfg.Agents {
		t.Error("saved defaults must reload identically")
	}
}

func TestMissingFileYieldsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.MaxRetries != 8 || cfg.Provider.StreamIdleTimeoutSecs != 180 {
		t.Errorf("provider defaults: %+v", cfg.Provider)
	}
	if cfg.Gateway.Port != 7643 || cfg.Session.HibernationIdleSecs != 1800 {
		t.Errorf("section defaults: %+v %+v", cfg.Gateway, cfg.Session)
	}
}

func TestDisplayLegacyCompat(t *testing.T) {
	cfg, err := LoadString(`
[display]
show_diffs = false
`)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Display.ApplyLegacyCompat()
	if cfg.Display.DiffMode != DiffOff {
		t.Errorf("legacy show_diffs=false must map to off, got %q", cfg.Display.DiffMode)
	}
}

func TestHookCommandsSingleVsMany(t *testing.T) {
	cfg, err := LoadString(`
[hooks]
turn_end = "say done"
pre_tool = ["gate.sh", "--strict"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if first, ok := cfg.Hooks.TurnEnd.First(); !ok || first != "say done" {
		t.Errorf("single hook: %v", cfg.Hooks.TurnEnd)
	}
	if len(cfg.Hooks.PreTool) != 2 {
		t.Errorf("multi hook: %v", cfg.Hooks.PreTool)
	}
	text, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), `turn_end = "say done"`) {
		t.Errorf("single hook must serialize as bare string:\n%s", text)
	}
}

// Live-machine check: the user's real ~/.jcode/config.toml must parse
// (nothing is written; this only validates interop reading).
func TestParseRealJcodeConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	raw, err := os.ReadFile(filepath.Join(home, ".jcode", "config.toml"))
	if err != nil {
		t.Skipf("no real jcode config: %v", err)
	}
	if _, err := LoadString(string(raw)); err != nil {
		t.Errorf("real config.toml must parse: %v", err)
	}
}

func strPtr(s string) *string  { return &s }
func uintPtr(v uint64) *uint64 { return &v }
