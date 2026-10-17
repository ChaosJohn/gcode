// Package sessionstore ports jcode-base/src/session/persistence.rs and
// jcode-storage: the session persistence model — a JSON snapshot per session
// (<id>.json) plus an append-only JSONL journal (<id>.journal.jsonl) — with
// journal replay, id dedupe, glued-line salvage, corrupt-journal recovery,
// checkpoint compaction, and jcode ~/.jcode interop (import).
package sessionstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/types"
)

// JournalMeta is the mutable session metadata carried by every journal
// entry (jcode SessionJournalMeta, field-compatible snake_case).
type JournalMeta struct {
	ParentID     *string `json:"parent_id,omitempty"`
	Title        *string `json:"title,omitempty"`
	CustomTitle  *string `json:"custom_title,omitempty"`
	SystemPrompt *string `json:"system_prompt,omitempty"`
	Model        *string `json:"model,omitempty"`
	Provider     *string `json:"provider,omitempty"`
	Status       *string `json:"status,omitempty"`
}

// JournalEntry is one append-only record (jcode SessionJournalEntry).
type JournalEntry struct {
	Meta                   JournalMeta           `json:"meta"`
	AppendMessages         []types.StoredMessage `json:"append_messages,omitempty"`
	AppendReplayEvents     []json.RawMessage     `json:"append_replay_events,omitempty"`
	AppendEnvSnapshots     []json.RawMessage     `json:"append_env_snapshots,omitempty"`
	AppendMemoryInjections []json.RawMessage     `json:"append_memory_injections,omitempty"`
}

// Session is the persisted store (jcode Session subset: transcript plus the
// metadata the journal mutates).
type Session struct {
	ID           string                `json:"id"`
	ParentID     *string               `json:"parent_id,omitempty"`
	Title        *string               `json:"title,omitempty"`
	CustomTitle  *string               `json:"custom_title,omitempty"`
	SystemPrompt *string               `json:"system_prompt,omitempty"`
	Model        *string               `json:"model,omitempty"`
	Provider     *string               `json:"provider,omitempty"`
	Status       string                `json:"status"`
	CreatedAt    *time.Time            `json:"created_at,omitempty"`
	UpdatedAt    *time.Time            `json:"updated_at,omitempty"`
	WorkingDir   string                `json:"working_dir,omitempty"`
	Messages     []types.StoredMessage `json:"messages"`
}

// HomeDir resolves the gcode home: GCODE_HOME wins, then ~/.gcode.
// JCODE_HOME is honored for import-jcode source lookups only.
func HomeDir() string {
	if home := os.Getenv("GCODE_HOME"); home != "" {
		return home
	}
	return filepath.Join(os.Getenv("HOME"), ".gcode")
}

// SessionPath ports session_path_in_dir: <home>/sessions/<id>.json.
func SessionPath(home, sessionID string) string {
	return filepath.Join(home, "sessions", fmt.Sprintf("%s.json", sessionID))
}

// JournalPathFromSnapshot ports session_journal_path_from_snapshot:
// <id>.json → <id>.journal.jsonl (stem suffix, not extension swap).
func JournalPathFromSnapshot(path string) string {
	dir, name := filepath.Split(path)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	return filepath.Join(dir, stem+".journal.jsonl")
}

// writeJSONAtomic ports read/write durability: temp file + rename.
func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readJSONWithRecovery ports storage::read_json_with_recovery: a corrupt
// primary falls back to the .backup sibling.
func readJSONWithRecovery(path string, target any) (recovered bool, err error) {
	raw, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := json.Unmarshal(raw, target); err == nil {
			return false, nil
		}
	}
	backup := path + ".backup"
	raw, readErr = os.ReadFile(backup)
	if readErr != nil {
		return false, fmt.Errorf("corrupt JSON at %s and no readable backup", path)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return false, fmt.Errorf("corrupt JSON at %s: backup also unreadable", path)
	}
	return true, nil
}

// Store couples an in-memory session with its persist state (jcode
// SessionPersistState): snapshot/journal split decisions.
type Store struct {
	Session Session
	path    string
	journal string
	// persist state
	snapshotExists    bool
	messagesLen       int
	saved             bool
	pendingCheckpoint bool
	// dedupe holds the last replay's stale-entry count (repair telemetry).
	dedupe *journalDedupe
}

// Open loads a session by id from the given home (ports load_from_path):
// snapshot replay, journal replay with dedupe and glued-line salvage, and
// duplicate repair bookkeeping.
func Open(home, sessionID string) (*Store, error) {
	path := SessionPath(home, sessionID)
	store := &Store{path: path, journal: JournalPathFromSnapshot(path)}
	recovered, err := readJSONWithRecovery(path, &store.Session)
	if err != nil {
		return nil, err
	}
	_ = recovered
	store.snapshotExists = true
	store.dedupeMessagesByID()
	store.dedupe = newJournalDedupe(store.Session.Messages)
	store.replayJournal(store.dedupe)
	store.messagesLen = len(store.Session.Messages)
	return store, nil
}

// NewStore prepares a fresh session for saving under home.
func NewStore(session Session, home string) *Store {
	path := SessionPath(home, session.ID)
	return &Store{Session: session, path: path, journal: JournalPathFromSnapshot(path)}
}

// Save persists the session (ports save_inner): checkpoint (full snapshot +
// journal truncation) when the transcript shrank or metadata changed;
// otherwise delta entries append to the journal.
func (s *Store) Save() error {
	s.Session.UpdatedAt = timePtr(time.Now())
	shrunk := s.snapshotExists && len(s.Session.Messages) < s.messagesLen
	if !s.snapshotExists || shrunk || s.pendingCheckpoint {
		if err := s.checkpointSnapshot(); err != nil {
			return err
		}
		return nil
	}
	entry := JournalEntry{
		Meta: JournalMeta{
			ParentID: s.Session.ParentID, Title: s.Session.Title,
			CustomTitle: s.Session.CustomTitle, SystemPrompt: s.Session.SystemPrompt,
			Model: s.Session.Model, Provider: s.Session.Provider,
			Status: strPtr(s.Session.Status),
		},
		AppendMessages: append([]types.StoredMessage(nil),
			s.Session.Messages[min(s.messagesLen, len(s.Session.Messages)):]...),
	}
	if err := appendJSONL(s.journal, entry); err != nil {
		return err
	}
	s.messagesLen = len(s.Session.Messages)
	s.saved = true
	return nil
}

func (s *Store) checkpointSnapshot() error {
	if err := writeJSONAtomic(s.path, s.Session); err != nil {
		return err
	}
	// Drop the folded journal only after the snapshot rename landed.
	_ = os.Remove(s.journal)
	s.snapshotExists = true
	s.messagesLen = len(s.Session.Messages)
	s.pendingCheckpoint = false
	s.saved = true
	return nil
}

func appendJSONL(path string, entry JournalEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func (s *Store) replayJournal(dedupe *journalDedupe) error {
	file, err := os.Open(s.journal)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	corrupt := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		entries := salvageGluedLines(line)
		for _, candidate := range entries {
			var entry JournalEntry
			if err := json.Unmarshal([]byte(candidate), &entry); err != nil {
				corrupt = true
				continue
			}
			if filtered, keep := dedupe.filter(entry); keep {
				s.applyJournalEntry(filtered)
			}
		}
	}
	if corrupt {
		// Ports schedule_checkpoint_after_corrupt_journal: the next save
		// rewrites a clean snapshot and drops the damaged journal.
		s.pendingCheckpoint = true
	}
	return scanner.Err()
}

// salvageGluedLines ports salvage_glued_journal_entries: torn writes can
// concatenate records; split on ENTRY_START boundaries.
func salvageGluedLines(line string) []string {
	const entryStart = `{"meta":`
	if !strings.Contains(line, entryStart) {
		return []string{line}
	}
	var out []string
	for {
		idx := strings.Index(line[1:], entryStart)
		if idx < 0 {
			break
		}
		out = append(out, line[:1+idx])
		line = line[1+idx:]
	}
	return append(out, line)
}

func (s *Store) applyJournalEntry(entry JournalEntry) {
	if entry.Meta.ParentID != nil {
		s.Session.ParentID = entry.Meta.ParentID
	}
	if entry.Meta.Title != nil {
		s.Session.Title = entry.Meta.Title
	}
	if entry.Meta.CustomTitle != nil {
		s.Session.CustomTitle = entry.Meta.CustomTitle
	}
	if entry.Meta.SystemPrompt != nil {
		s.Session.SystemPrompt = entry.Meta.SystemPrompt
	}
	if entry.Meta.Model != nil {
		s.Session.Model = entry.Meta.Model
	}
	if entry.Meta.Provider != nil {
		s.Session.Provider = entry.Meta.Provider
	}
	if entry.Meta.Status != nil {
		s.Session.Status = *entry.Meta.Status
	}
	s.Session.Messages = append(s.Session.Messages, entry.AppendMessages...)
}

func (s *Store) dedupeMessagesByID() {
	seen := map[string]bool{}
	kept := s.Session.Messages[:0]
	for _, message := range s.Session.Messages {
		if message.ID != "" {
			if seen[message.ID] {
				continue
			}
			seen[message.ID] = true
		}
		kept = append(kept, message)
	}
	s.Session.Messages = kept
}

// journalDedupe ports JournalReplayDedupe: entries replaying messages whose
// ids are already in the snapshot are stale, not duplicates of each other.
type journalDedupe struct {
	seen  map[string]bool
	stale int
}

func newJournalDedupe(messages []types.StoredMessage) *journalDedupe {
	seen := map[string]bool{}
	for _, message := range messages {
		if message.ID != "" {
			seen[message.ID] = true
		}
	}
	return &journalDedupe{seen: seen}
}

func (d *journalDedupe) filter(entry JournalEntry) (JournalEntry, bool) {
	kept := entry.AppendMessages[:0]
	for _, message := range entry.AppendMessages {
		if message.ID != "" {
			if d.seen[message.ID] {
				d.stale++
				continue
			}
			d.seen[message.ID] = true
		}
		kept = append(kept, message)
	}
	entry.AppendMessages = kept
	return entry, true
}

// ListSessions enumerates <home>/sessions/*.json with titles, newest first.
func ListSessions(home string) ([]Session, error) {
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") ||
			strings.HasSuffix(entry.Name(), ".backup") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(home, "sessions", entry.Name()))
		if err != nil {
			continue
		}
		var session Session
		if json.Unmarshal(raw, &session) != nil {
			continue
		}
		sessions = append(sessions, session)
	}
	// Newest first by updated_at.
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && newer(sessions[j], sessions[j-1]); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
	return sessions, nil
}

func newer(a, b Session) bool {
	ta, tb := time.Time{}, time.Time{}
	if a.UpdatedAt != nil {
		ta = *a.UpdatedAt
	}
	if b.UpdatedAt != nil {
		tb = *b.UpdatedAt
	}
	return ta.After(tb)
}

// ImportJcode copies jcode sessions from ~/.jcode (or $JCODE_HOME) into the
// gcode home. Files are format-identical (same snapshot+journal layout), so
// the copy is byte-preserving; IDs already present in gcode are skipped.
func ImportJcode(sourceHome, targetHome string) (imported int, skipped int, err error) {
	entries, err := os.ReadDir(filepath.Join(sourceHome, "sessions"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if err := os.MkdirAll(filepath.Join(targetHome, "sessions"), 0o755); err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") ||
			strings.HasSuffix(entry.Name(), ".backup") {
			continue
		}
		target := filepath.Join(targetHome, "sessions", entry.Name())
		if _, err := os.Stat(target); err == nil {
			skipped++
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sourceHome, "sessions", entry.Name()))
		if err != nil {
			continue
		}
		if err := os.WriteFile(target, raw, 0o600); err != nil {
			return imported, skipped, err
		}
		imported++
		// Carry the journal alongside when present.
		journal := JournalPathFromSnapshot(entry.Name())
		if raw, err := os.ReadFile(filepath.Join(sourceHome, "sessions", journal)); err == nil {
			_ = os.WriteFile(filepath.Join(targetHome, "sessions",
				JournalPathFromSnapshot(filepath.Base(target))), raw, 0o600)
		}
	}
	return imported, skipped, nil
}

func timePtr(t time.Time) *time.Time { return &t }
func strPtr(s string) *string        { return &s }
