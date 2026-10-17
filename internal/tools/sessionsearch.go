// Package tools: session_search (ported from tool/session_search.rs +
// jcode-session-types renderer): scans the gcode sessions directory with the
// snapshot+journal loader and returns per-session transcript hits.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// session_search bounds (ported constants).
const (
	sessionSearchDefaultLimit      = 10
	sessionSearchMaxLimit          = 50
	sessionSearchDefaultPerSession = 1
	sessionSearchMaxPerSession     = 20
	sessionSearchMaxScanSessions   = 10000
	sessionSearchMaxContext        = 5
)

// SessionSearchTool searches past chat sessions.
type SessionSearchTool struct{}

// NewSessionSearchTool builds the tool.
func NewSessionSearchTool() *SessionSearchTool { return &SessionSearchTool{} }

// Name implements Tool.
func (t *SessionSearchTool) Name() string { return "session_search" }

// Description implements Tool.
func (t *SessionSearchTool) Description() string {
	return "Search past chat sessions. Current session and tool noise hidden by default."
}

// ParametersSchema implements Tool.
func (t *SessionSearchTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {"type": "string", "description": "Search query. Use distinctive keywords; stop-word-only queries are rejected."},
			"working_dir": {"type": "string", "description": "Only sessions whose working directory matches this path prefix (case-insensitive)."},
			"limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Max results."},
			"include_current": {"type": "boolean", "description": "Include the current active session. Defaults to false."},
			"include_tools": {"type": "boolean", "description": "Include raw tool calls and tool results. Defaults to false to reduce log noise."},
			"include_system": {"type": "boolean", "description": "Include system reminders and display/system messages. Defaults to false."},
			"max_per_session": {"type": "integer", "minimum": 1, "maximum": 20, "description": "Maximum hits to return from one session. Defaults to 1 for result diversity."},
			"role": {"type": "string", "enum": ["all", "user", "assistant"], "description": "Restrict results to a role. Defaults to all transcript roles."},
			"context_before": {"type": "integer", "minimum": 0, "maximum": 5, "description": "Transcript messages shown before each hit."},
			"context_after": {"type": "integer", "minimum": 0, "maximum": 5, "description": "Transcript messages shown after each hit."}
		}
	}`)
}

// queryProfile ports QueryProfile: lowercased term split + stop-word filter.
// The raw text is kept so the empty-query and stop-word-only errors stay
// distinct (upstream is_empty vs !is_actionable).
type queryProfile struct {
	raw   string
	terms []string
}

var searchStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"of": true, "to": true, "in": true, "on": true, "for": true, "with": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"it": true, "this": true, "that": true, "these": true, "those": true,
	"i": true, "you": true, "he": true, "she": true, "we": true, "they": true,
	"my": true, "your": true, "me": true, "at": true, "by": true, "as": true,
	"can": true, "do": true, "does": true, "did": true, "not": true, "no": true,
	"yes": true, "if": true, "then": true, "so": true, "what": true, "how": true,
	"why": true, "when": true, "where": true, "who": true, "please": true,
}

func newQueryProfile(raw string) queryProfile {
	var terms []string
	for _, field := range strings.Fields(strings.ToLower(raw)) {
		field = strings.Trim(field, ".,;:!?\"'()`")
		if field == "" || searchStopWords[field] {
			continue
		}
		terms = append(terms, field)
	}
	return queryProfile{raw: raw, terms: terms}
}

func (q queryProfile) isEmpty() bool      { return strings.TrimSpace(q.raw) == "" }
func (q queryProfile) isActionable() bool { return len(q.terms) > 0 }

// searchHit is one transcript match.
type searchHit struct {
	SessionID  string
	Title      *string
	WorkingDir string
	Model      *string
	Role       string
	Index      int
	MessageID  string
	Snippet    string
	Context    []string
	UpdatedAt  *time.Time
}

// Execute implements Tool.
func (t *SessionSearchTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		Query          string  `json:"query"`
		WorkingDir     *string `json:"working_dir"`
		Limit          *int    `json:"limit"`
		IncludeCurrent *bool   `json:"include_current"`
		IncludeTools   *bool   `json:"include_tools"`
		IncludeSystem  *bool   `json:"include_system"`
		MaxPerSession  *int    `json:"max_per_session"`
		Role           *string `json:"role"`
		ContextBefore  *int    `json:"context_before"`
		ContextAfter   *int    `json:"context_after"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	limit := sessionSearchDefaultLimit
	if params.Limit != nil {
		limit = clampInt(*params.Limit, 1, sessionSearchMaxLimit)
	}
	maxPerSession := sessionSearchDefaultPerSession
	if params.MaxPerSession != nil {
		maxPerSession = clampInt(*params.MaxPerSession, 1, sessionSearchMaxPerSession)
		if maxPerSession > limit {
			maxPerSession = limit
		}
	}
	contextBefore, contextAfter := 0, 0
	if params.ContextBefore != nil {
		contextBefore = clampInt(*params.ContextBefore, 0, sessionSearchMaxContext)
	}
	if params.ContextAfter != nil {
		contextAfter = clampInt(*params.ContextAfter, 0, sessionSearchMaxContext)
	}
	roleFilter := "all"
	if params.Role != nil {
		roleFilter = *params.Role
	}
	includeTools := params.IncludeTools != nil && *params.IncludeTools
	includeSystem := params.IncludeSystem != nil && *params.IncludeSystem
	includeCurrent := params.IncludeCurrent != nil && *params.IncludeCurrent

	query := newQueryProfile(params.Query)
	if query.isEmpty() {
		return toolcore.NewToolOutput("Query cannot be empty.").WithTitle("session_search"), nil
	}
	if !query.isActionable() {
		return toolcore.NewToolOutput(fmt.Sprintf(
			"Query '%s' is too generic after removing common stop words. Add at least one distinctive keyword.",
			strings.TrimSpace(params.Query))).WithTitle("session_search"), nil
	}

	home := sessionstore.HomeDir()
	ids, _ := sessionstore.ListSessions(home)
	var hits []searchHit
	scanned := 0
	for _, listed := range ids {
		if scanned >= sessionSearchMaxScanSessions || len(hits) >= limit {
			break
		}
		if !includeCurrent && listed.ID == ctx.SessionID {
			continue
		}
		if params.WorkingDir != nil && *params.WorkingDir != "" &&
			!strings.Contains(strings.ToLower(listed.WorkingDir), strings.ToLower(*params.WorkingDir)) {
			continue
		}
		store, err := sessionstore.Open(home, listed.ID)
		if err != nil {
			continue
		}
		scanned++
		perSession := 0
		for index, message := range store.Session.Messages {
			if perSession >= maxPerSession || len(hits) >= limit {
				break
			}
			if roleFilter != "all" && string(message.Role) != roleFilter {
				continue
			}
			snippet := message.ContentPreview()
			if snippet == "(empty)" {
				continue
			}
			if !includeTools && (strings.HasPrefix(snippet, "[tool:") ||
				strings.HasPrefix(snippet, "[result:")) {
				continue
			}
			lowered := strings.ToLower(snippet)
			matched := false
			for _, term := range query.terms {
				if strings.Contains(lowered, term) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			var context []string
			for offset := -contextBefore; offset <= contextAfter; offset++ {
				pos := index + offset
				if pos < 0 || pos >= len(store.Session.Messages) || pos == index {
					continue
				}
				context = append(context, store.Session.Messages[pos].ContentPreview())
			}
			hits = append(hits, searchHit{
				SessionID: listed.ID, Title: listed.CustomTitle,
				WorkingDir: listed.WorkingDir, Model: listed.Model,
				Role: string(message.Role), Index: index, MessageID: message.ID,
				Snippet: snippet, Context: context, UpdatedAt: listed.UpdatedAt,
			})
			perSession++
		}
	}

	if len(hits) == 0 {
		return toolcore.NewToolOutput(fmt.Sprintf(
			"No session matches for '%s'. Try distinctive keywords from the earlier conversation.",
			strings.TrimSpace(params.Query))).WithTitle("session_search"), nil
	}
	// Ported format_session_search_results.
	var out strings.Builder
	fmt.Fprintf(&out, "## Found %d results for '%s'\n\n", len(hits), strings.TrimSpace(params.Query))
	fmt.Fprintf(&out, "_Defaults: current session %s, tool calls/results %s, system reminders %s. Max per session: %d._\n\n",
		ternary(includeCurrent, "included", "excluded"),
		ternary(includeTools, "included", "hidden"),
		ternary(includeSystem, "included", "hidden"), maxPerSession)
	fmt.Fprintf(&out, "_Scanned: %d sessions._\n\n", scanned)
	for i, hit := range hits {
		name := hit.SessionID
		if hit.Title != nil && *hit.Title != "" {
			name = *hit.Title
		}
		fmt.Fprintf(&out, "### Result %d - %s\n", i+1, name)
		fmt.Fprintf(&out, "- Source: `jcode`\n- Session ID: `%s`\n", hit.SessionID)
		if hit.Title != nil && *hit.Title != "" {
			fmt.Fprintf(&out, "- Title: %s\n", *hit.Title)
		}
		if hit.WorkingDir != "" {
			fmt.Fprintf(&out, "- Working dir: `%s`\n", hit.WorkingDir)
		}
		if hit.Model != nil && *hit.Model != "" {
			fmt.Fprintf(&out, "- Model: `%s`\n", *hit.Model)
		}
		updated := ""
		if hit.UpdatedAt != nil {
			updated = hit.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		}
		fmt.Fprintf(&out, "- Updated: %s\n- Match: %s #%d (%s), id `%s`\n",
			updated, hit.Role, hit.Index+1, hit.Role, hit.MessageID)
		fmt.Fprintf(&out, "- Snippet: %s\n", hit.Snippet)
		for _, line := range hit.Context {
			fmt.Fprintf(&out, "  | %s\n", line)
		}
		out.WriteString("\n")
	}
	return toolcore.NewToolOutput(out.String()).WithTitle("session_search"), nil
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
