package types

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// --- task/goal types (crates/jcode-task-types) ------------------------------

// GoalScope mirrors the snake_case enum (global | project, default project).
type GoalScope string

const (
	GoalScopeGlobal  GoalScope = "global"
	GoalScopeProject GoalScope = "project"
)

var _ json.Unmarshaler = (*GoalScope)(nil)

func (g *GoalScope) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch GoalScope(strings.ToLower(strings.TrimSpace(s))) {
	case GoalScopeGlobal:
		*g = GoalScopeGlobal
	case "", GoalScopeProject:
		*g = GoalScopeProject
	default:
		return fmt.Errorf("unknown goal scope %q", s)
	}
	return nil
}

// GoalStatus mirrors the snake_case enum with sort ranks.
type GoalStatus string

const (
	GoalDraft     GoalStatus = "draft"
	GoalActive    GoalStatus = "active"
	GoalPaused    GoalStatus = "paused"
	GoalBlocked   GoalStatus = "blocked"
	GoalCompleted GoalStatus = "completed"
	GoalArchived  GoalStatus = "archived"
	GoalAbandoned GoalStatus = "abandoned"
)

var _ json.Unmarshaler = (*GoalStatus)(nil)

func (g *GoalStatus) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch GoalStatus(strings.ToLower(strings.TrimSpace(s))) {
	case "", GoalActive:
		*g = GoalActive
	case GoalDraft:
		*g = GoalDraft
	case GoalPaused:
		*g = GoalPaused
	case GoalBlocked:
		*g = GoalBlocked
	case GoalCompleted:
		*g = GoalCompleted
	case GoalArchived:
		*g = GoalArchived
	case GoalAbandoned:
		*g = GoalAbandoned
	default:
		return fmt.Errorf("unknown goal status %q", s)
	}
	return nil
}

// SortRank mirrors GoalStatus::sort_rank.
func (g GoalStatus) SortRank() int {
	switch g {
	case GoalActive:
		return 0
	case GoalBlocked:
		return 1
	case GoalDraft:
		return 2
	case GoalPaused:
		return 3
	case GoalCompleted:
		return 4
	case GoalArchived:
		return 5
	case GoalAbandoned:
		return 6
	}
	return 2
}

// IsResumable mirrors GoalStatus::is_resumable.
func (g GoalStatus) IsResumable() bool {
	return g == GoalActive || g == GoalBlocked || g == GoalDraft
}

// GoalStep / GoalMilestone / GoalUpdate / Goal mirror the persisted structs.
type GoalStep struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

type GoalMilestone struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status string     `json:"status"`
	Steps  []GoalStep `json:"steps,omitempty"`
}

type GoalUpdate struct {
	At      JTime  `json:"at"`
	Summary string `json:"summary"`
}

type Goal struct {
	ID                 string          `json:"id"`
	Title              string          `json:"title"`
	Scope              GoalScope       `json:"scope"`
	Status             GoalStatus      `json:"status"`
	Description        string          `json:"description"`
	Why                string          `json:"why"`
	SuccessCriteria    []string        `json:"success_criteria,omitempty"`
	Milestones         []GoalMilestone `json:"milestones,omitempty"`
	NextSteps          []string        `json:"next_steps,omitempty"`
	Blockers           []string        `json:"blockers,omitempty"`
	CurrentMilestoneID *string         `json:"current_milestone_id,omitempty"`
	ProgressPercent    *uint8          `json:"progress_percent,omitempty"`
	CreatedAt          JTime           `json:"created_at"`
	UpdatedAt          JTime           `json:"updated_at"`
	Updates            []GoalUpdate    `json:"updates,omitempty"`
}

// NewGoal mirrors Goal::new (id derived from the trimmed title).
func NewGoal(title string, scope GoalScope) Goal {
	now := Now()
	trimmed := strings.TrimSpace(title)
	if scope == "" {
		scope = GoalScopeProject
	}
	return Goal{
		ID:        SanitizeGoalID(trimmed),
		Title:     trimmed,
		Scope:     scope,
		Status:    GoalActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// SanitizeGoalID mirrors sanitize_goal_id + slugify.
func SanitizeGoalID(id string) string {
	slug := slugifyGoal(id)
	if slug == "" {
		return "goal"
	}
	return slug
}

func slugifyGoal(input string) string {
	var b strings.Builder
	prevDash := false
	for _, ch := range input {
		lower := ch
		if ch >= 'A' && ch <= 'Z' {
			lower = ch + ('a' - 'A')
		}
		isAlnum := (lower >= 'a' && lower <= 'z') || (lower >= '0' && lower <= '9')
		if isAlnum {
			b.WriteRune(lower)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// --- semantic_state enums ---------------------------------------------------
//
// Port of task-types' semantic_state! macro: serialize as a snake_case
// string, but also accept legacy 0-100 numeric scores (numbers and numeric
// strings) from before the semantic-state migration, mapping onto the
// closest state via legacy ranges.

// SemanticLevel is implemented by semantic_state enums.
type SemanticLevel interface {
	Label() string
	LegacyScore() uint8
	LegacyRanges() []legacyRange
}

type legacyRange struct {
	lo, hi int
	value  SemanticLevel
}

// parseSemanticState accepts a state string or a legacy numeric score.
func parseSemanticState(data []byte, name string, all []SemanticLevel) (SemanticLevel, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		for _, level := range all {
			if level.Label() == trimmed {
				return level, nil
			}
		}
		// Legacy numeric score sent as a string.
		if score, err := strconv.ParseFloat(trimmed, 64); err == nil && score >= 0 && score <= 100 {
			return fromLegacyScore(score, all), nil
		}
		return nil, fmt.Errorf("invalid %s state: %s", name, v)
	case float64:
		if v < 0 || v > 100 {
			return nil, fmt.Errorf("legacy %s score out of 0-100 range", name)
		}
		return fromLegacyScore(v, all), nil
	default:
		return nil, fmt.Errorf("%s must be a state string, got %v", name, raw)
	}
}

func fromLegacyScore(score float64, all []SemanticLevel) SemanticLevel {
	clamped := int(score)
	if clamped > 100 {
		clamped = 100
	}
	for _, level := range all {
		for _, r := range level.LegacyRanges() {
			if clamped >= r.lo && clamped <= r.hi {
				return r.value
			}
		}
	}
	return all[len(all)-1]
}

// IntentUnderstanding: how well the agent understands what the user wants.
type IntentUnderstanding string

const (
	IntentUncertain IntentUnderstanding = "uncertain"
	IntentPartial   IntentUnderstanding = "partial"
	IntentClear     IntentUnderstanding = "clear"
	IntentComplete  IntentUnderstanding = "complete"
)

// SemanticLevel implementation.
func (v IntentUnderstanding) Label() string { return string(v) }
func (v IntentUnderstanding) LegacyScore() uint8 {
	return map[IntentUnderstanding]uint8{IntentUncertain: 40, IntentPartial: 80, IntentClear: 96, IntentComplete: 100}[v]
}
func (v IntentUnderstanding) LegacyRanges() []legacyRange {
	return []legacyRange{
		{0, 59, IntentUncertain},
		{60, 95, IntentPartial},
		{96, 99, IntentClear},
		{100, 100, IntentComplete},
	}
}

var intentLevels = []SemanticLevel{IntentUncertain, IntentPartial, IntentClear, IntentComplete}

var _ json.Marshaler = IntentUnderstanding("")
var _ json.Unmarshaler = (*IntentUnderstanding)(nil)

func (v IntentUnderstanding) MarshalJSON() ([]byte, error) { return json.Marshal(string(v)) }

func (v *IntentUnderstanding) UnmarshalJSON(data []byte) error {
	level, err := parseSemanticState(data, "IntentUnderstanding", intentLevels)
	if err != nil {
		return err
	}
	*v = level.(IntentUnderstanding)
	return nil
}

// ConfidenceState: evidence state behind a todo, from unexamined guess to
// end-to-end verified.
type ConfidenceState string

const (
	ConfidenceSpeculative ConfidenceState = "speculative"
	ConfidencePlausible   ConfidenceState = "plausible"
	ConfidenceValidated   ConfidenceState = "validated"
	ConfidenceVerified    ConfidenceState = "verified"
)

func (v ConfidenceState) Label() string { return string(v) }
func (v ConfidenceState) LegacyScore() uint8 {
	return map[ConfidenceState]uint8{ConfidenceSpeculative: 40, ConfidencePlausible: 80, ConfidenceValidated: 96, ConfidenceVerified: 100}[v]
}
func (v ConfidenceState) LegacyRanges() []legacyRange {
	return []legacyRange{
		{0, 59, ConfidenceSpeculative},
		{60, 95, ConfidencePlausible},
		{96, 99, ConfidenceValidated},
		{100, 100, ConfidenceVerified},
	}
}

var confidenceLevels = []SemanticLevel{ConfidenceSpeculative, ConfidencePlausible, ConfidenceValidated, ConfidenceVerified}

var _ json.Marshaler = ConfidenceState("")
var _ json.Unmarshaler = (*ConfidenceState)(nil)

func (v ConfidenceState) MarshalJSON() ([]byte, error) { return json.Marshal(string(v)) }

func (v *ConfidenceState) UnmarshalJSON(data []byte) error {
	level, err := parseSemanticState(data, "ConfidenceState", confidenceLevels)
	if err != nil {
		return err
	}
	*v = level.(ConfidenceState)
	return nil
}

// --- todo plan types --------------------------------------------------------

// TodoItem mirrors the persisted todo item.
type TodoItem struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	ID       string `json:"id"`
	// Group groups todos under a single header; start a new group when work
	// is steered into a new area instead of renaming.
	Group *string `json:"group,omitempty"`
	// Confidence is forward-looking evidence that this todo can be completed
	// correctly; legacy sessions stored a 0-100 score.
	Confidence *ConfidenceState `json:"confidence,omitempty"`
	// CompletionConfidence is recorded when the todo is marked completed.
	CompletionConfidence *ConfidenceState `json:"completion_confidence,omitempty"`
	// ConfidenceHistory is every distinct state, oldest first, ending with
	// the current one. Maintained by the todo tool, not the model.
	ConfidenceHistory []ConfidenceState `json:"confidence_history,omitempty"`
	BlockedBy         []string          `json:"blocked_by,omitempty"`
	AssignedTo        *string           `json:"assigned_to,omitempty"`
}

// TodoPlan is plan-level understanding of user intent.
type TodoPlan struct {
	UserIntention *string `json:"user_intention,omitempty"`
	// UnderstandsUserIntent accepts legacy 0-100 scores under this name or
	// the alignment_score / user_intention_alignment aliases.
	UnderstandsUserIntent        *IntentUnderstanding  `json:"understands_user_intent,omitempty"`
	UnderstandsUserIntentHistory []IntentUnderstanding `json:"understands_user_intent_history,omitempty"`
}

// TodoPlanField names a plan field changed by a todo-tool update.
type TodoPlanField string

const (
	PlanFieldUserIntention         TodoPlanField = "user_intention"
	PlanFieldUnderstandsUserIntent TodoPlanField = "understands_user_intent"
)

var _ json.Unmarshaler = (*TodoPlanField)(nil)

func (f *TodoPlanField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch s {
	case "user_intention":
		*f = PlanFieldUserIntention
	case "understands_user_intent", "alignment_score", "user_intention_alignment":
		*f = PlanFieldUnderstandsUserIntent
	default:
		return fmt.Errorf("unknown todo plan field %q", s)
	}
	return nil
}

// TodoPlanChange is before/after state for plan-level intent.
type TodoPlanChange struct {
	Before *TodoPlan       `json:"before,omitempty"`
	After  *TodoPlan       `json:"after,omitempty"`
	Fields []TodoPlanField `json:"fields"`
}

// --- usage types (crates/jcode-usage-types, serde-persisted part) -----------

// CopilotUsageTracker is persisted Copilot quota accounting.
type CopilotUsageTracker struct {
	Today   DayUsage     `json:"today"`
	Month   MonthUsage   `json:"month"`
	AllTime AllTimeUsage `json:"all_time"`
}

// DayUsage is one calendar day of Copilot usage.
type DayUsage struct {
	Date            string `json:"date"`
	Requests        uint64 `json:"requests"`
	PremiumRequests uint64 `json:"premium_requests"`
	InputTokens     uint64 `json:"input_tokens"`
	OutputTokens    uint64 `json:"output_tokens"`
}

// MonthUsage is one month of Copilot usage.
type MonthUsage struct {
	Month           string `json:"month"`
	Requests        uint64 `json:"requests"`
	PremiumRequests uint64 `json:"premium_requests"`
	InputTokens     uint64 `json:"input_tokens"`
	OutputTokens    uint64 `json:"output_tokens"`
}

// AllTimeUsage is lifetime Copilot usage.
type AllTimeUsage struct {
	Requests        uint64 `json:"requests"`
	PremiumRequests uint64 `json:"premium_requests"`
	InputTokens     uint64 `json:"input_tokens"`
	OutputTokens    uint64 `json:"output_tokens"`
}

// TelemetryToolCategory mirrors classify_telemetry_tool_category.
type TelemetryToolCategory string

const (
	CategoryReadSearch TelemetryToolCategory = "read_search"
	CategoryWrite      TelemetryToolCategory = "write"
	CategoryShell      TelemetryToolCategory = "shell"
	CategoryWeb        TelemetryToolCategory = "web"
	CategoryMemory     TelemetryToolCategory = "memory"
	CategorySubagent   TelemetryToolCategory = "subagent"
	CategorySwarm      TelemetryToolCategory = "swarm"
	CategoryEmail      TelemetryToolCategory = "email"
	CategorySidePanel  TelemetryToolCategory = "side_panel"
	CategoryGoal       TelemetryToolCategory = "goal"
	CategoryTodo       TelemetryToolCategory = "todo"
	CategoryMCP        TelemetryToolCategory = "mcp"
	CategoryOther      TelemetryToolCategory = "other"
)

var telemetryToolCategories = map[string]TelemetryToolCategory{
	"read": CategoryReadSearch, "glob": CategoryReadSearch, "grep": CategoryReadSearch,
	"agentgrep": CategoryReadSearch, "ls": CategoryReadSearch,
	"conversation_search": CategoryReadSearch, "session_search": CategoryReadSearch,
	"write": CategoryWrite, "edit": CategoryWrite, "multiedit": CategoryWrite,
	"patch": CategoryWrite, "apply_patch": CategoryWrite, "replace": CategoryWrite,
	"bash": CategoryShell, "bg": CategoryShell, "schedule": CategoryShell,
}

// ClassifyTelemetryToolCategory ports the read/search, write, and shell
// classes exactly; the remaining categories are finalized with the tool
// registry in M3.
func ClassifyTelemetryToolCategory(name string) TelemetryToolCategory {
	if c, ok := telemetryToolCategories[name]; ok {
		return c
	}
	return CategoryOther
}
