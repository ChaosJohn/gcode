package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Persistence wiring (M4): the agent stores its transcript through the
// sessionstore snapshot+journal model, jcode-compatible on disk.

// ToStoredMessage converts a transcript message for persistence, deriving a
// stable id from role+index+content hash (jcode ids are content-stable too).
func ToStoredMessage(message types.Message, index int) types.StoredMessage {
	hasher := sha256.New()
	hasher.Write([]byte(message.Role))
	raw, _ := json.Marshal(message.Content)
	hasher.Write(raw)
	sum := hex.EncodeToString(hasher.Sum(nil))[:16]
	stored := types.StoredMessage{
		ID:             fmt.Sprintf("msg-%d-%s", index, sum),
		Role:           message.Role,
		Content:        message.Content,
		ToolDurationMs: message.ToolDurationMs,
	}
	if message.Timestamp != nil {
		ts := *message.Timestamp
		stored.Timestamp = &ts
	} else {
		now := types.JTime{Time: time.Now()}
		stored.Timestamp = &now
	}
	return stored
}

// WirePersistence attaches a sessionstore.Store: after every tool round the
// full transcript is synced and saved (checkpoint or journal append).
// The persisted transcript uses the derived stable ids, so repeated saves
// of the same prefix stay dedupe-safe under journal replay.
func (a *Agent) WirePersistence(store *sessionstore.Store) {
	a.persistStore = store
}

// PersistTranscript syncs the in-memory transcript into the store and saves.
func (a *Agent) PersistTranscript() error {
	if a.persistStore == nil {
		return nil
	}
	stored := make([]types.StoredMessage, 0, len(a.Messages))
	for i, message := range a.Messages {
		stored = append(stored, ToStoredMessage(message, i))
	}
	a.persistStore.Session.Messages = stored
	a.persistStore.Session.WorkingDir = a.WorkingDir
	a.persistStore.Session.Model = strPtrOrNil(a.Provider.Model())
	a.persistStore.Session.Provider = strPtrOrNil(a.Provider.Name())
	if a.persistStore.Session.Status == "" {
		a.persistStore.Session.Status = "active"
	}
	return a.persistStore.Save()
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
