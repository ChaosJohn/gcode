// Package memory ports the jcode memory core (jcode-memory-types +
// jcode-base/src/memory.rs, storage subset): scoped JSON stores, duplicate
// reinforcement, normalized-text search, and the prompt renderer. The
// sidecar pipeline (embeddings, LLM relevance judge, memory graph links)
// is out of scope — see the batch report.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Category is one memory category (jcode MemoryCategory).
type Category string

// Memory categories.
const (
	CategoryFact       Category = "fact"
	CategoryPreference Category = "preference"
	CategoryEntity     Category = "entity"
	CategoryCorrection Category = "correction"
)

// ValidCategory checks a user-provided category.
func ValidCategory(value string) bool {
	switch Category(value) {
	case CategoryFact, CategoryPreference, CategoryEntity, CategoryCorrection:
		return true
	}
	return false
}

// Scope selects which stores a query touches.
type Scope string

// Memory scopes.
const (
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
	ScopeAll     Scope = "all"
)

// ParseScope validates a scope string (default all).
func ParseScope(value string) (Scope, error) {
	switch Scope(value) {
	case ScopeProject, ScopeGlobal, ScopeAll:
		return Scope(value), nil
	}
	return "", fmt.Errorf("Unknown scope: %s. Use project, global, or all", value)
}

// Entry is one stored memory (field names match jcode's serde output so a
// jcode-produced store file parses unchanged).
type Entry struct {
	ID           string    `json:"id"`
	Category     Category  `json:"category"`
	Content      string    `json:"content"`
	Tags         []string  `json:"tags,omitempty"`
	SearchText   string    `json:"search_text,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	AccessCount  int       `json:"access_count"`
	Source       string    `json:"source,omitempty"`
	Active       bool      `json:"active"`
	SupersededBy string    `json:"superseded_by,omitempty"`
}

// Store is one scope's on-disk memory file.
type Store struct {
	Entries  []Entry           `json:"entries"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Manager reads and writes the scoped stores under the memory home.
type Manager struct {
	home       string
	projectDir string
}

// NewManager builds a manager over ~/.gcode (GCODE_HOME respected).
func NewManager() *Manager {
	home := os.Getenv("GCODE_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".gcode")
	}
	return &Manager{home: home}
}

// WithProjectDir scopes project lookups to a working directory.
func (m *Manager) WithProjectDir(dir string) *Manager {
	clone := *m
	clone.projectDir = dir
	return &clone
}

// projectHash ports the DefaultHasher scheme's role: a stable 16-hex key
// for the working directory (FNV-1a 64; hashes differ from jcode's SipHash,
// so project store files do not cross-reference by path — see report).
func (m *Manager) projectHash() string {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(m.projectDir))
	return fmt.Sprintf("%016x", hasher.Sum64())
}

func (m *Manager) projectPath() string {
	return filepath.Join(m.home, "memory", "projects", m.projectHash()+".json")
}

func (m *Manager) globalPath() string {
	return filepath.Join(m.home, "memory", "global.json")
}

func loadStore(path string) Store {
	store := Store{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return store
	}
	_ = json.Unmarshal(raw, &store)
	return store
}

func saveStore(path string, store Store) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

// LoadProject returns the project store (empty when no working dir).
func (m *Manager) LoadProject() Store {
	if m.projectDir == "" {
		return Store{}
	}
	return loadStore(m.projectPath())
}

// LoadGlobal returns the global store.
func (m *Manager) LoadGlobal() Store { return loadStore(m.globalPath()) }

func (m *Manager) saveProject(store Store) error {
	if m.projectDir == "" {
		return fmt.Errorf("Project memory requires a working directory; use global scope explicitly")
	}
	return saveStore(m.projectPath(), store)
}

func (m *Manager) saveGlobal(store Store) error {
	return saveStore(m.globalPath(), store)
}

func newID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "mem_" + hex.EncodeToString(raw)
}

// rememberInStore ports remember_in_graph: same category + trimmed content
// reinforces the duplicate instead of adding a row.
func rememberInStore(store *Store, entry *Entry) string {
	normalized := strings.TrimSpace(entry.Content)
	for i := range store.Entries {
		existing := &store.Entries[i]
		if existing.Active && existing.Category == entry.Category &&
			strings.TrimSpace(existing.Content) == normalized {
			existing.AccessCount++
			existing.UpdatedAt = time.Now().UTC()
			return existing.ID
		}
	}
	entry.ID = newID()
	entry.Content = normalized
	entry.CreatedAt = time.Now().UTC()
	entry.UpdatedAt = entry.CreatedAt
	entry.Active = true
	entry.SearchText = NormalizeSearchText(entry.Content + " " + strings.Join(entry.Tags, " "))
	store.Entries = append(store.Entries, *entry)
	return entry.ID
}

// Remember stores one memory in the requested scope and returns its id.
func (m *Manager) Remember(scope Scope, entry Entry) (string, error) {
	switch scope {
	case ScopeProject:
		store := m.LoadProject()
		id := rememberInStore(&store, &entry)
		return id, m.saveProject(store)
	case ScopeGlobal:
		store := m.LoadGlobal()
		id := rememberInStore(&store, &entry)
		return id, m.saveGlobal(store)
	}
	return "", fmt.Errorf("unsupported scope %s", scope)
}

// collect merges the active entries of the requested scopes.
func (m *Manager) collect(scope Scope) []Entry {
	var entries []Entry
	if scope == ScopeProject || scope == ScopeAll {
		if m.projectDir != "" {
			entries = append(entries, m.LoadProject().Entries...)
		}
	}
	if scope == ScopeGlobal || scope == ScopeAll {
		entries = append(entries, m.LoadGlobal().Entries...)
	}
	active := entries[:0]
	for _, entry := range entries {
		if entry.Active {
			active = append(active, entry)
		}
	}
	return active
}

// Recent returns the most recently updated active memories.
func (m *Manager) Recent(scope Scope, limit int) []Entry {
	entries := m.collect(scope)
	sort.SliceStable(entries, func(a, b int) bool {
		return entries[a].UpdatedAt.After(entries[b].UpdatedAt)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// Search filters active entries by normalized substring match over content
// and tags (ported search_scoped).
func (m *Manager) Search(scope Scope, query string) []Entry {
	needle := NormalizeSearchText(query)
	if needle == "" {
		return nil
	}
	var results []Entry
	for _, entry := range m.collect(scope) {
		if strings.Contains(NormalizeSearchText(entry.Content+" "+strings.Join(entry.Tags, " ")), needle) {
			results = append(results, entry)
		}
	}
	sort.SliceStable(results, func(a, b int) bool {
		return results[a].UpdatedAt.After(results[b].UpdatedAt)
	})
	return results
}

// Forget deactivates one memory by id (ported forget).
func (m *Manager) Forget(scope Scope, id string) bool {
	updated := false
	touch := func(store *Store, save func(Store) error) {
		for i := range store.Entries {
			if store.Entries[i].ID == id && store.Entries[i].Active {
				store.Entries[i].Active = false
				store.Entries[i].UpdatedAt = time.Now().UTC()
				updated = true
			}
		}
		if updated {
			_ = save(*store)
		}
	}
	if scope == ScopeProject || scope == ScopeAll {
		if m.projectDir != "" {
			store := m.LoadProject()
			touch(&store, m.saveProject)
		}
	}
	if !updated && (scope == ScopeGlobal || scope == ScopeAll) {
		store := m.LoadGlobal()
		touch(&store, m.saveGlobal)
	}
	return updated
}

// Tag adds tags to one memory (ported tag_memory).
func (m *Manager) Tag(scope Scope, id string, tags []string) bool {
	updated := false
	touch := func(store *Store, save func(Store) error) {
		for i := range store.Entries {
			if store.Entries[i].ID != id {
				continue
			}
			entry := &store.Entries[i]
			for _, tag := range tags {
				duplicate := false
				for _, existing := range entry.Tags {
					if strings.EqualFold(existing, tag) {
						duplicate = true
						break
					}
				}
				if !duplicate {
					entry.Tags = append(entry.Tags, tag)
				}
			}
			entry.SearchText = NormalizeSearchText(entry.Content + " " + strings.Join(entry.Tags, " "))
			entry.UpdatedAt = time.Now().UTC()
			updated = true
		}
		if updated {
			_ = save(*store)
		}
	}
	if scope == ScopeProject || scope == ScopeAll {
		if m.projectDir != "" {
			store := m.LoadProject()
			touch(&store, m.saveProject)
		}
	}
	if !updated && (scope == ScopeGlobal || scope == ScopeAll) {
		store := m.LoadGlobal()
		touch(&store, m.saveGlobal)
	}
	return updated
}

// NormalizeSearchText ports normalize_search_text: lowercase, separators
// mapped to spaces, whitespace collapsed.
func NormalizeSearchText(text string) string {
	normalized := strings.Builder{}
	lastWasSpace := true
	for _, ch := range strings.ToLower(strings.TrimSpace(text)) {
		mapped := ch
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' ||
			ch == '-' || ch == '_' || ch == '/' || ch == '\\' || ch == '.' || ch == ':' {
			mapped = ' '
		}
		if mapped == ' ' {
			if lastWasSpace {
				continue
			}
			normalized.WriteRune(' ')
			lastWasSpace = true
			continue
		}
		normalized.WriteRune(mapped)
		lastWasSpace = false
	}
	return strings.TrimRight(normalized.String(), " ")
}

// FormatForPrompt ports format_entries_for_prompt: category sections in
// Correction/Fact/Preference/Entity order, numbered contents.
func FormatForPrompt(entries []Entry, limit int) string {
	if limit > 0 {
		// recent-first ordering is the caller's responsibility; cap here.
		if len(entries) > limit {
			entries = entries[:limit]
		}
	}
	order := []Category{CategoryCorrection, CategoryFact,
		CategoryPreference, CategoryEntity}
	sections := map[Category][]Entry{}
	for _, entry := range entries {
		sections[entry.Category] = append(sections[entry.Category], entry)
	}
	var output strings.Builder
	writeSection := func(title string, items []Entry) {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		fmt.Fprintf(&output, "## %s\n", title)
		for idx, item := range items {
			fmt.Fprintf(&output, "%d. %s\n", idx+1, strings.TrimSpace(item.Content))
		}
	}
	titles := map[Category]string{
		CategoryCorrection: "Corrections", CategoryFact: "Facts",
		CategoryPreference: "Preferences", CategoryEntity: "Entities",
	}
	for _, category := range order {
		if items, ok := sections[category]; ok {
			writeSection(titles[category], items)
			delete(sections, category)
		}
	}
	// Remaining categories (custom) sorted for determinism.
	var rest []Category
	for category := range sections {
		rest = append(rest, category)
	}
	sort.Slice(rest, func(a, b int) bool { return rest[a] < rest[b] })
	for _, category := range rest {
		writeSection(string(category), sections[category])
	}
	return strings.TrimRight(output.String(), "\n")
}
