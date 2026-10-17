// Package tools ports jcode's core tools (read/write/edit/ls and friends).
// Each tool keeps upstream's exact output shapes; the swarm bus events become
// a settable observer hook (the swarm coordinator is M4+ work).
package tools

import (
	"encoding/json"
	"sync"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// FileTouch describes one file mutation for swarm coordination.
type FileTouch struct {
	SessionID string
	Path      string
	// Op is one of "read", "write", "edit", "delete".
	Op      string
	Intent  string
	Summary string
	Detail  string
}

var (
	fileTouchMu sync.RWMutex
	fileTouchFn func(FileTouch)
)

// SetFileTouchObserver wires the swarm bus in (nil disables).
func SetFileTouchObserver(fn func(FileTouch)) {
	fileTouchMu.Lock()
	defer fileTouchMu.Unlock()
	fileTouchFn = fn
}

func publishFileTouch(touch FileTouch) {
	fileTouchMu.RLock()
	fn := fileTouchFn
	fileTouchMu.RUnlock()
	if fn != nil {
		fn(touch)
	}
}

// Locking: upstream file_lock.rs serializes mutations per path within the
// process. Go map of mutexes under one guard.

var (
	fileLocksMu sync.Mutex
	fileLocks   = map[string]*sync.Mutex{}
)

func lockPath(path string) func() {
	fileLocksMu.Lock()
	mutex := fileLocks[path]
	if mutex == nil {
		mutex = &sync.Mutex{}
		fileLocks[path] = mutex
	}
	fileLocksMu.Unlock()
	mutex.Lock()
	return mutex.Unlock
}

// parseInput decodes tool input JSON into a struct.
func parseInput(input json.RawMessage, target any) error {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return json.Unmarshal(input, target)
}

// intentOf extracts the optional intent label.
func intentOf(input map[string]json.RawMessage) string {
	var intent string
	if raw, ok := input["intent"]; ok {
		_ = json.Unmarshal(raw, &intent)
	}
	return intent
}

// rawIntent returns the trimmed intent, or "".
func trimmedIntent(input json.RawMessage) string {
	var params struct {
		Intent string `json:"intent"`
	}
	_ = parseInput(input, &params)
	return trimSpace(params.Intent)
}

var _ = toolcore.EnsureIntentInSchema
