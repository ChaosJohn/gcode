// Package config mirrors jcode's configuration layer (crates/jcode-config-types
// and crates/jcode-base/src/config.rs): a lenient, defaults-first TOML config
// whose unknown sections are ignored on load and whose saved form carries
// every field.
//
// Parity notes (see docs/port/DECISIONS.md):
//   - Every section is #[serde(default)]: gcode pre-fills a Default Config and
//     unmarshals over it, so absent sections keep their defaults.
//   - Enums are deliberately lenient (jcode issue #349): an unknown spelling
//     falls back to the section default instead of failing the whole config.
//   - jcode passes the `[desktop]` table through untouched; gcode also keeps
//     any other unknown top-level table in Extra and writes it back on save
//     (a documented superset of upstream, which drops them).
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the root of ~/.jcode/config.toml (read) / ~/.gcode/config.toml
// (written). Field order mirrors the Rust struct declaration order, which is
// the order jcode's toml serializer emits.
type Config struct {
	Session       SessionConfig                  `toml:"session"`
	Server        ServerConfig                   `toml:"server"`
	Keybindings   KeybindingsConfig              `toml:"keybindings"`
	Dictation     DictationConfig                `toml:"dictation"`
	Display       DisplayConfig                  `toml:"display"`
	Features      FeatureConfig                  `toml:"features"`
	WebSearch     WebSearchConfig                `toml:"websearch"`
	Tools         ToolConfig                     `toml:"tools"`
	Acp           AcpConfig                      `toml:"acp"`
	Auth          AuthConfig                     `toml:"auth"`
	Provider      ProviderConfig                 `toml:"provider"`
	Providers     map[string]NamedProviderConfig `toml:"providers"`
	Agents        AgentsConfig                   `toml:"agents"`
	Terminal      TerminalConfig                 `toml:"terminal"`
	Hooks         HooksConfig                    `toml:"hooks"`
	Ambient       AmbientConfig                  `toml:"ambient"`
	Safety        SafetyConfig                   `toml:"safety"`
	Notifications NotificationsConfig            `toml:"notifications"`
	Gateway       GatewayConfig                  `toml:"gateway"`
	Compaction    CompactionConfig               `toml:"compaction"`
	Power         PowerConfig                    `toml:"power"`
	Menubar       MenubarConfig                  `toml:"menubar"`
	AutoReview    AutoReviewConfig               `toml:"autoreview"`
	AutoJudge     AutoJudgeConfig                `toml:"autojudge"`
	Sponsors      SponsorsConfig                 `toml:"sponsors"`
	LaunchHotkeys LaunchHotkeysConfig            `toml:"launch_hotkeys"`
	// Desktop is the passthrough `[desktop]` table owned by Jcode Desktop.
	Desktop map[string]any `toml:"desktop,omitempty"`
	// Extra holds unknown top-level tables so reloads and saves never lose
	// user data jcode itself would drop. Keyed by table name.
	Extra map[string]any `toml:"-"`
}

// Default returns the all-defaults config, equal to what jcode produces when
// every section is missing.
func Default() *Config {
	return &Config{
		Session:       defaultSessionConfig(),
		Server:        defaultServerConfig(),
		Keybindings:   DefaultKeybindings(runtimePlatform()),
		Dictation:     defaultDictationConfig(),
		Display:       defaultDisplayConfig(),
		Features:      defaultFeatureConfig(),
		WebSearch:     defaultWebSearchConfig(),
		Tools:         defaultToolConfig(),
		Acp:           defaultAcpConfig(),
		Auth:          AuthConfig{},
		Provider:      defaultProviderConfig(),
		Providers:     map[string]NamedProviderConfig{},
		Agents:        defaultAgentsConfig(),
		Terminal:      TerminalConfig{},
		Hooks:         defaultHooksConfig(),
		Ambient:       defaultAmbientConfig(),
		Safety:        defaultSafetyConfig(),
		Notifications: defaultNotificationsConfig(),
		Gateway:       defaultGatewayConfig(),
		Compaction:    defaultCompactionConfig(),
		Power:         defaultPowerConfig(),
		Menubar:       defaultMenubarConfig(),
		AutoReview:    AutoReviewConfig{},
		AutoJudge:     AutoJudgeConfig{},
		Sponsors:      defaultSponsorsConfig(),
		LaunchHotkeys: LaunchHotkeysConfig{},
	}
}

// Load reads and parses path over the defaults. A missing file yields the
// all-defaults config (jcode treats a missing config.toml the same way).
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := cfg.unmarshalTOMLBytes(data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// LoadString parses TOML text over the defaults (tests and doctor).
func LoadString(text string) (*Config, error) {
	cfg := Default()
	if err := cfg.unmarshalTOMLBytes([]byte(text)); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) unmarshalTOMLBytes(data []byte) error {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return err
	}
	normalizeTOMLDoc(doc)
	if c.Extra == nil {
		c.Extra = map[string]any{}
	}
	known := knownTopLevelTables()
	for key, value := range doc {
		if _, ok := known[key]; !ok {
			if tbl, ok := value.(map[string]any); ok {
				c.Extra[key] = tbl
			}
		}
	}
	reencoded, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	return toml.Unmarshal(reencoded, c)
}

// Save writes the config back as TOML, matching jcode's save: every field is
// serialized (defaults included), Extra tables appended.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	text, err := c.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, text, 0o600)
}

// Marshal encodes the config (typed fields + Extra tables merged in).
// NOTE: deliberately not named MarshalTOML: that exact signature matches
// go-toml's unstable.Marshaler and would recurse under EnableMarshalerInterface.
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf).EnableMarshalerInterface()
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if len(c.Extra) > 0 {
		extraBytes, err := toml.Marshal(c.Extra)
		if err != nil {
			return nil, err
		}
		out = append(out, '\n')
		out = append(out, extraBytes...)
	}
	return out, nil
}

// knownTopLevelTables lists the sections Config decodes; anything else is a
// passthrough table.
func knownTopLevelTables() map[string]struct{} {
	return map[string]struct{}{
		"session": {}, "server": {}, "keybindings": {}, "dictation": {},
		"display": {}, "features": {}, "websearch": {}, "tools": {},
		"acp": {}, "auth": {}, "provider": {}, "providers": {}, "agents": {},
		"terminal": {}, "hooks": {}, "ambient": {}, "safety": {},
		"notifications": {}, "gateway": {}, "compaction": {}, "power": {},
		"menubar": {}, "autoreview": {}, "autojudge": {}, "sponsors": {},
		"launch_hotkeys": {}, "desktop": {},
	}
}

// tomlAliases maps legacy/alternate spellings onto canonical snake_case keys
// (jcode declares serde aliases for these).
var tomlAliases = map[string]string{
	"extra-body":       "extra_body",
	"reasoning-effort": "reasoning_effort",
}

// enumRules normalizes enum-valued config fields before decode. go-toml
// assigns strings into string-kinded Go types directly (its TextUnmarshaler
// hook only fires for non-string kinds), so leniency is applied here: an
// unknown spelling is replaced by the section default, a value with no safe
// fallback is dropped (jcode issue #349 philosophy).
var enumRules = map[string]func(any) (any, bool){
	"features.update_channel":          enumRule(ParseUpdateChannel, string(ChannelStable)),
	"server.wake_mode":                 enumRule(ParseWakeMode, string(WakeInternal)),
	"display.diff_mode":                enumRule(ParseDiffDisplayMode, string(DiffInline)),
	"display.diagram_mode":             enumRule(ParseDiagramDisplayMode, string(DiagramNone)),
	"display.markdown_spacing":         enumRule(ParseMarkdownSpacingMode, string(SpacingCompact)),
	"display.latex_rendering":          enumRule(ParseLatexRenderingMode, string(LatexImage)),
	"display.reasoning_display":        enumRule(ParseReasoningDisplayMode, ""), // invalid → drop (None semantics)
	"provider.cross_provider_failover": enumRule(ParseCrossProviderFailoverMode, string(FailoverCountdown)),
	"agents.swarm_spawn_mode":          enumRule(ParseSwarmSpawnMode, string(SwarmSpawnInline)),
	"agents.swarm_strip_layout":        enumRule(ParseSwarmStripLayout, string(SwarmStripVertical)),
	"tools.mcp_tools":                  enumRule(ParseMcpToolsMode, string(McpToolsAuto)),
	"keybindings.session_picker_enter": enumRule(ParseSessionPickerResumeAction, string(ResumeCurrentTerminal)),
	"websearch.engine":                 enumRule(ParseWebSearchEngine, string(WebSearchDuckDuckGo)),
	"websearch.custom.method":          enumRule(ParseCustomWebSearchMethod, string(CustomWebSearchGet)),
	"dictation.mode":                   enumRule(ParseTranscriptMode, string(TranscriptSend)),
	"compaction.mode":                  enumRule(ParseCompactionMode, string(CompactionReactive)),
}

func enumRule[T ~string](parse func(string) (T, bool), fallback string) func(any) (any, bool) {
	return func(value any) (any, bool) {
		text, ok := value.(string)
		if !ok {
			return value, true // non-string: let go-toml report the type error
		}
		if canon, ok := parse(text); ok {
			return string(canon), true
		}
		if fallback == "" {
			return nil, false // drop the key entirely
		}
		return fallback, true
	}
}

// normalizeTOMLDoc applies alias renames and enum leniency in place.
func normalizeTOMLDoc(doc map[string]any) {
	normalizeNode("", doc)
}

func normalizeNode(path string, node any) {
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			if canon, ok := tomlAliases[key]; ok {
				v[canon] = value
				delete(v, key)
				key = canon
			}
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if rule, ok := enumRules[childPath]; ok {
				if normalized, keep := rule(value); keep {
					v[key] = normalized
				} else {
					delete(v, key)
				}
				continue
			}
			if childPath == "providers" {
				if providers, ok := value.(map[string]any); ok {
					normalizeProviders(providers)
				}
			}
			if childPath == "hooks" {
				if hooks, ok := value.(map[string]any); ok {
					normalizeHooks(hooks)
				}
			}
			normalizeNode(childPath, value)
		}
	case []any:
		for _, item := range v {
			normalizeNode(path, item)
		}
	}
}

// normalizeProviders applies the per-provider enum rules.
func normalizeProviders(providers map[string]any) {
	for _, entry := range providers {
		table, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if raw, ok := table["provider_type"]; ok {
			if normalized, keep := enumRule(ParseNamedProviderType, string(NamedProviderOpenAICompatible))(raw); keep {
				table["provider_type"] = normalized
			} else {
				delete(table, "provider_type")
			}
		}
		if raw, ok := table["auth"]; ok {
			if normalized, keep := enumRule(ParseNamedProviderAuth, string(NamedAuthBearer))(raw); keep {
				table["auth"] = normalized
			} else {
				delete(table, "auth")
			}
		}
	}
}
