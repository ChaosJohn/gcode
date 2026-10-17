// Ported persistence tests (jcode-base/src/session/persistence tests subset
// + storage recovery): snapshot/journal roundtrip, replay dedupe, glued
// salvage, corrupt-journal checkpoint, checkpoint shrink, jcode import.
package sessionstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

func textMessage(id, text string) types.StoredMessage {
	return types.StoredMessage{ID: id, Role: types.RoleUser,
		Content: []types.ContentBlock{types.TextBlock(text)}}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	home := t.TempDir()
	store := NewStore(Session{ID: "s1", Status: "active", WorkingDir: "/tmp"}, home)
	store.Session.Messages = []types.StoredMessage{textMessage("m1", "hello")}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	store.Session.Messages = append(store.Session.Messages, textMessage("m2", "world"))
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	// First save checkpoints the snapshot; second appends one journal entry.
	reopened, err := Open(home, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Session.Messages) != 2 ||
		*reopened.Session.Messages[1].Content[0].Text != "world" {
		t.Fatalf("messages: %+v", reopened.Session.Messages)
	}
	if _, err := os.Stat(SessionPath(home, "s1")); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := os.Stat(JournalPathFromSnapshot(SessionPath(home, "s1"))); err != nil {
		t.Fatalf("journal: %v", err)
	}
}

// A shrunken transcript forces a checkpoint (snapshot rewrite + journal drop).
func TestCheckpointOnShrink(t *testing.T) {
	home := t.TempDir()
	store := NewStore(Session{ID: "s2", Status: "active"}, home)
	store.Session.Messages = []types.StoredMessage{
		textMessage("m1", "one"), textMessage("m2", "two"), textMessage("m3", "three")}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	store.Session.Messages = store.Session.Messages[:1]
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.journal); !os.IsNotExist(err) {
		t.Fatalf("journal must be dropped after checkpoint: %v", err)
	}
	reopened, err := Open(home, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Session.Messages) != 1 {
		t.Fatalf("messages: %d", len(reopened.Session.Messages))
	}
}

// Journal entries replaying ids already in the snapshot are dropped as stale.
func TestJournalReplayDedupe(t *testing.T) {
	home := t.TempDir()
	store := NewStore(Session{ID: "s3", Status: "active"}, home)
	store.Session.Messages = []types.StoredMessage{textMessage("m1", "hello")}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	// Append a journal entry by hand that replays m1.
	stale := JournalEntry{Meta: JournalMeta{Status: strPtr("active")},
		AppendMessages: []types.StoredMessage{textMessage("m1", "hello")}}
	if err := appendJSONL(store.journal, stale); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home, "s3")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Session.Messages) != 1 {
		t.Fatalf("duplicate replayed: %d", len(reopened.Session.Messages))
	}
	if reopened.dedupe == nil || reopened.dedupe.stale == 0 {
		t.Fatalf("stale counter: %+v", reopened.dedupe)
	}
}

// Torn (glued) journal lines salvage both records.
func TestSalvageGluedLines(t *testing.T) {
	line := `{"meta":{"status":"active"},"append_messages":[{"id":"a","role":"user","content":[{"type":"text","text":"x"}]}]}{"meta":{"status":"active"},"append_messages":[{"id":"b","role":"user","content":[{"type":"text","text":"y"}]}]}`
	entries := salvageGluedLines(line)
	if len(entries) != 2 {
		t.Fatalf("salvaged: %d", len(entries))
	}
	if !strings.HasPrefix(entries[1], `{"meta"`) {
		t.Fatalf("second entry: %s", entries[1])
	}
}

// A corrupt journal line schedules a checkpoint instead of failing the load.
func TestCorruptJournalRecovers(t *testing.T) {
	home := t.TempDir()
	store := NewStore(Session{ID: "s4", Status: "active"}, home)
	store.Session.Messages = []types.StoredMessage{textMessage("m1", "hello")}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.journal,
		[]byte("NOT JSON AT ALL\n"+`{"meta":{"status":"active"},"append_messages":[{"id":"m2","role":"user","content":[{"type":"text","text":"good"}]}]}`+"\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home, "s4")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Session.Messages) != 2 {
		t.Fatalf("messages: %d", len(reopened.Session.Messages))
	}
	if !reopened.pendingCheckpoint {
		t.Fatalf("checkpoint not scheduled")
	}
	if err := reopened.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(reopened.journal); !os.IsNotExist(err) {
		t.Fatalf("journal not dropped")
	}
}

// Corrupt snapshot with a good .backup recovers.
func TestSnapshotBackupRecovery(t *testing.T) {
	home := t.TempDir()
	store := NewStore(Session{ID: "s5", Status: "active"}, home)
	store.Session.Messages = []types.StoredMessage{textMessage("m1", "saved")}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	snapshot := SessionPath(home, "s5")
	raw, _ := os.ReadFile(snapshot)
	if err := os.WriteFile(snapshot+".backup", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home, "s5")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Session.Messages) != 1 {
		t.Fatalf("backup replay: %d", len(reopened.Session.Messages))
	}
}

// ListSessions orders by updated_at, newest first.
func TestListSessions(t *testing.T) {
	home := t.TempDir()
	first := NewStore(Session{ID: "old", Status: "active"}, home)
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	second := NewStore(Session{ID: "new", Status: "active", CustomTitle: strPtr("Newest")}, home)
	if err := second.Save(); err != nil {
		t.Fatal(err)
	}
	sessions, err := ListSessions(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != "new" {
		t.Fatalf("order: %+v", sessions)
	}
}

// ImportJcode copies snapshots + journals, skipping existing ids.
func TestImportJcode(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	// Emulate the jcode home layout: the source file exists, the target
	// sessions directory does not yet.
	jcodeSessions := filepath.Join(source, "sessions")
	if err := os.MkdirAll(jcodeSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jcodeSessions, "j1.json"), []byte(`{"id":"j1","status":"active","messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jcodeSessions, "j1.journal.jsonl"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	imported, skipped, err := ImportJcode(source, target)
	if err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("import: %d/%d %v", imported, skipped, err)
	}
	// Re-import skips.
	imported, skipped, err = ImportJcode(source, target)
	if err != nil || imported != 0 || skipped != 1 {
		t.Fatalf("re-import: %d/%d %v", imported, skipped, err)
	}
	reopened, err := Open(target, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Session.ID != "j1" {
		t.Fatalf("imported session: %+v", reopened.Session)
	}
}
