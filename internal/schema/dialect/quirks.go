package dialect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Persisting what a provider rejected, so it is only learned once
// (ported from quirks.rs). The store is advisory: a corrupt or unreadable file
// degrades to "learn it again", never to a failure.

type quirkEntry struct {
	RejectedKeywords []string `json:"rejected_keywords,omitempty"`
	RejectedFormats  []string `json:"rejected_formats,omitempty"`
}

type quirkFile struct {
	Dialects map[string]quirkEntry `json:"dialects"`
}

var (
	quirksMu       sync.Mutex
	quirksTestPath string
)

// SetStorePathForTest pins the store location for tests.
func SetStorePathForTest(path string) {
	quirksMu.Lock()
	defer quirksMu.Unlock()
	quirksTestPath = path
}

// storePath must be called with quirksMu held.
func storePath() (string, bool) {
	if quirksTestPath != "" {
		return filepath.Join(quirksTestPath, "schema-quirks.json"), true
	}
	if explicit := os.Getenv("GCODE_SCHEMA_QUIRKS_PATH"); explicit != "" {
		return explicit, true
	}
	home := os.Getenv("GCODE_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		home = filepath.Join(userHome, ".gcode")
	}
	return filepath.Join(home, "schema-quirks.json"), true
}

func readStore() quirkFile {
	var file quirkFile
	path, ok := storePath()
	if !ok {
		return file
	}
	if content, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(content, &file)
	}
	if file.Dialects == nil {
		file.Dialects = map[string]quirkEntry{}
	}
	return file
}

func writeStore(file quirkFile) {
	path, ok := storePath()
	if !ok {
		return
	}
	if file.Dialects == nil {
		file.Dialects = map[string]quirkEntry{}
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o644)
}

// LearnedFor returns what has been learned for a dialect id.
func LearnedFor(dialectID string) LearnedQuirks {
	quirksMu.Lock()
	defer quirksMu.Unlock()
	file := readStore()
	entry := file.Dialects[dialectID]
	return LearnedQuirks{
		RejectedKeywords: entry.RejectedKeywords,
		RejectedFormats:  entry.RejectedFormats,
	}
}

// RecordKeyword persists a rejected keyword; true when newly learned.
func RecordKeyword(dialectID, keyword string) bool {
	if keyword == "" {
		return false
	}
	quirksMu.Lock()
	defer quirksMu.Unlock()
	file := readStore()
	entry := file.Dialects[dialectID]
	for _, known := range entry.RejectedKeywords {
		if known == keyword {
			return false
		}
	}
	entry.RejectedKeywords = append(entry.RejectedKeywords, keyword)
	file.Dialects[dialectID] = entry
	writeStore(file)
	return true
}

// RecordFormat persists a rejected format; true when newly learned.
func RecordFormat(dialectID, format string) bool {
	if format == "" {
		return false
	}
	quirksMu.Lock()
	defer quirksMu.Unlock()
	file := readStore()
	entry := file.Dialects[dialectID]
	for _, known := range entry.RejectedFormats {
		if known == format {
			return false
		}
	}
	entry.RejectedFormats = append(entry.RejectedFormats, format)
	file.Dialects[dialectID] = entry
	writeStore(file)
	return true
}

// Normalize is the provider entry point: normalize for spec, honoring learned
// quirks.
func Normalize(schema any, spec *DialectSpec) any {
	learned := LearnedFor(spec.ID)
	return ApplyWithQuirks(schema, spec, &learned)
}

// NormalizeSchemaJSON normalizes a raw JSON tool schema.
func NormalizeSchemaJSON(raw json.RawMessage, spec *DialectSpec) json.RawMessage {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return raw
	}
	normalized, err := marshalStable(Normalize(decoded, spec))
	if err != nil {
		return raw
	}
	return normalized
}
