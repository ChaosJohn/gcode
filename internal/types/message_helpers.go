package types

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ToolOutputMissingText mirrors TOOL_OUTPUT_MISSING_TEXT.
const ToolOutputMissingText = "Tool output missing (session interrupted before tool execution completed)"

// FNV-1a 64-bit parameters — jcode's stable hash (STABLE_HASH_SEED/PRIME).
const (
	fnvSeed  uint64 = 0xcbf29ce484222325
	fnvPrime uint64 = 0x100000001b3
)

// StableHashBytes is jcode's stable_hash_bytes (FNV-1a 64).
func StableHashBytes(b []byte) uint64 {
	h := fnvSeed
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime
	}
	return h
}

// ExtendStableHash mirrors extend_stable_hash: hash(le64(acc) || le64(next)).
func ExtendStableHash(acc, next uint64) uint64 {
	var buf [16]byte
	lePutUint64(buf[0:8], acc)
	lePutUint64(buf[8:16], next)
	return StableHashBytes(buf[:])
}

// StableMessageHash mirrors stable_message_hash: FNV-1a over the serialized
// message bytes. Serialization stays field-order compatible with serde.
func StableMessageHash(m Message) uint64 {
	b, err := MarshalCompact(m)
	if err != nil {
		return StableHashBytes([]byte(fmt.Sprintf("%+v", m)))
	}
	return StableHashBytes(b)
}

// CacheRelevantMessageValue projects a message down to the fields that affect
// a provider KV-cache key: drops timestamp/tool_duration_ms, strips
// history-only reasoning_trace blocks, and removes cache_control breakpoints
// from text blocks. Faithful port of cache_relevant_message_value; encoded in
// struct field order for hash stability.
func CacheRelevantMessageValue(m Message) json.RawMessage {
	clone := Message{
		Role:    m.Role,
		Content: make([]ContentBlock, 0, len(m.Content)),
	}
	for _, block := range m.Content {
		if block.Type == BlockReasoningTrace {
			continue
		}
		if block.Type == BlockText && block.CacheControl != nil {
			block.CacheControl = nil
		}
		clone.Content = append(clone.Content, block)
	}
	out, err := MarshalCompact(clone)
	if err != nil {
		return json.RawMessage("null")
	}
	return out
}

// CacheRelevantMessages projects a whole list (see CacheRelevantMessageValue).
func CacheRelevantMessages(messages []Message) []json.RawMessage {
	out := make([]json.RawMessage, len(messages))
	for i, m := range messages {
		out[i] = CacheRelevantMessageValue(m)
	}
	return out
}

// CacheRelevantMessageHashes hashes each projected message with FNV-1a over
// the compact JSON encoding. Upstream hashes with Rust's DefaultHasher
// (SipHash) instead; hash values are process-local prefix-change detectors
// (never persisted, see the #[serde(skip)] caches), so the digest function
// differs by design — the projection itself is identical.
func CacheRelevantMessageHashes(messages []Message) []uint64 {
	out := make([]uint64, len(messages))
	for i, m := range messages {
		out[i] = StableHashBytes(CacheRelevantMessageValue(m))
	}
	return out
}

// WithTimestamps injects UTC timestamps into user-role text content: tool
// results get a timing header, user text gets a [timestamp] prefix on the
// first text block. Internal system-reminder messages are skipped. Port of
// Message::with_timestamps.
func WithTimestamps(messages []Message) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		msg := out[i]
		if msg.Timestamp == nil || msg.Role != RoleUser || isInternalSystemReminder(msg) {
			continue
		}
		tag := fmt.Sprintf("[%s]", FormatTimestamp(msg.Timestamp.Time))
		var toolTag string
		if msg.ToolDurationMs != nil {
			start := msg.Timestamp.Add(-time.Duration(*msg.ToolDurationMs) * time.Millisecond)
			toolTag = fmt.Sprintf("[tool timing: start=%s finish=%s duration=%s]",
				FormatTimestamp(start), FormatTimestamp(msg.Timestamp.Time), FormatDuration(*msg.ToolDurationMs))
		} else {
			toolTag = fmt.Sprintf("[%s]", FormatTimestamp(msg.Timestamp.Time))
		}
		// Deep-copy the content slice: callers hold the original blocks and
		// upstream clones the Vec before tagging.
		blocks := make([]ContentBlock, len(msg.Content))
		copy(blocks, msg.Content)
		tagged := false
		for j := range blocks {
			b := &blocks[j]
			if b.Type == BlockText && b.Text != nil && !tagged {
				taggedText := tag + " " + *b.Text
				b.Text = &taggedText
				tagged = true
			} else if b.Type == BlockToolResult && b.Content != nil && !tagged {
				taggedContent := toolTag + " " + *b.Content
				b.Content = &taggedContent
				tagged = true
			}
		}
		msg.Content = blocks
		out[i] = msg
	}
	return out
}

// IsInternalSystemReminder reports whether the first text block starts with
// <system-reminder>.
func (m Message) IsInternalSystemReminder() bool { return isInternalSystemReminder(m) }

func isInternalSystemReminder(m Message) bool {
	for _, block := range m.Content {
		if block.Text != nil {
			return strings.HasPrefix(strings.TrimLeft(*block.Text, " \t\n\r"), "<system-reminder>")
		}
	}
	return false
}

// FormatDuration mirrors Message::format_duration.
func FormatDuration(durationMs uint64) string {
	switch {
	case durationMs <= 999:
		return fmt.Sprintf("%dms", durationMs)
	case durationMs <= 9_999:
		return fmt.Sprintf("%.1fs", float64(durationMs)/1000.0)
	case durationMs <= 59_999:
		return fmt.Sprintf("%ds", durationMs/1000)
	default:
		totalSeconds := durationMs / 1000
		minutes := totalSeconds / 60
		seconds := totalSeconds % 60
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
}

// EndsWithFreshUserTurn mirrors ends_with_fresh_user_turn: the transcript
// ends with a real (non-system-reminder) user prompt, not a tool result.
func EndsWithFreshUserTurn(messages []Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != RoleUser {
			return false
		}
		for _, block := range msg.Content {
			if block.Type == BlockToolResult {
				return false
			}
		}
		if len(msg.Content) == 0 {
			return false
		}
		sawUserText := false
		onlyText := true
		for _, block := range msg.Content {
			if block.Type != BlockText {
				onlyText = false
				break
			}
			trimmed := strings.TrimSpace(deref(block.Text))
			if trimmed != "" && !strings.HasPrefix(trimmed, "<system-reminder>") {
				sawUserText = true
			}
		}
		if !onlyText {
			return false
		}
		if sawUserText {
			return true
		}
		if isInternalSystemReminder(msg) {
			continue
		}
		return false
	}
	return false
}

// MessagesWithDynamicSystemContext inserts dynamic system context after the
// latest fresh user prompt without disturbing the cached history prefix.
func MessagesWithDynamicSystemContext(messages []Message, systemDynamic string) []Message {
	trimmed := strings.TrimSpace(systemDynamic)
	if trimmed == "" {
		return messages
	}
	dynamic := NewUserMessage(fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", trimmed))
	insertAt := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if isFreshUserTextMessage(messages[i]) {
			insertAt = i + 1
			break
		}
	}
	out := make([]Message, 0, len(messages)+1)
	out = append(out, messages[:insertAt]...)
	out = append(out, dynamic)
	out = append(out, messages[insertAt:]...)
	return out
}

func isFreshUserTextMessage(m Message) bool {
	if m.Role != RoleUser {
		return false
	}
	sawUserText := false
	for _, block := range m.Content {
		switch block.Type {
		case BlockText:
			trimmed := strings.TrimSpace(deref(block.Text))
			if trimmed != "" && !strings.HasPrefix(trimmed, "<system-reminder>") {
				sawUserText = true
			}
		case BlockImage:
			// images ride along
		default:
			return false
		}
	}
	return sawUserText
}

// ReplaceTextSuffix applies a provider's late text correction only to an
// exact trailing match; returns the replaced byte offset. Port of
// replace_text_suffix (stale/mismatched snapshots are rejected).
func ReplaceTextSuffix(text *string, oldText, newText string) (int, bool) {
	if oldText == "" || !strings.HasSuffix(*text, oldText) {
		return 0, false
	}
	start := len(*text) - len(oldText)
	*text = (*text)[:start] + newText
	return start, true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func lePutUint64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
}
