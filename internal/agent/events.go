package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Kind is the type of a normalized event.
type Kind string

// Event kinds. Payload type is noted for each.
const (
	KindRunStarted          Kind = "run_started"          // RunStarted (host-emitted)
	KindUserMessage         Kind = "user_message"         // UserMessageData (returned by SendUser)
	KindSessionInit         Kind = "session_init"         // SessionInit (deduplicated; may repeat when tools/mode change)
	KindTextDelta           Kind = "text_delta"           // BlockDelta
	KindThinkingDelta       Kind = "thinking_delta"       // BlockDelta
	KindMessageDone         Kind = "message_done"         // MessageDone (a text/thinking block finished, carries final text)
	KindToolStart           Kind = "tool_start"           // ToolStart (input may still be empty)
	KindToolInputDelta      Kind = "tool_input_delta"     // ToolInputDelta
	KindToolInputDone       Kind = "tool_input_done"      // ToolInputDone (complete input; creates the tool if unseen)
	KindToolResult          Kind = "tool_result"          // ToolResult
	KindPermissionRequest   Kind = "permission_request"   // PermissionRequest
	KindPermissionResolved  Kind = "permission_resolved"  // PermissionResolved
	KindPermissionCancelled Kind = "permission_cancelled" // PermissionCancelled (provider withdrew the request)
	KindPlanProposal        Kind = "plan_proposal"        // PlanProposal (answer via RespondPermission)
	KindTodoUpdate          Kind = "todo_update"          // TodoUpdate
	KindUsage               Kind = "usage"                // Usage (per turn)
	KindStatus              Kind = "status"               // Status
	KindModeChanged         Kind = "mode_changed"         // ModeChanged
	KindModelChanged        Kind = "model_changed"        // ModelChanged
	KindTaskUpdate          Kind = "task_update"          // TaskUpdate
	KindModelsList          Kind = "models_list"          // ModelsList
	KindError               Kind = "error"                // ErrorData
	KindRunResult           Kind = "run_result"           // RunResult (end of turn)
)

// Error codes used in ErrorData.Code.
const (
	ErrResumeNotFound = "resume_not_found"
	ErrRunFailed      = "run_error"
	ErrControl        = "control_error"
	ErrProcessExit    = "process_exit"
	ErrProtocol       = "protocol_error"
)

// Event is one normalized, replayable agent event. Sessions leave Seq, RunID
// and TS zero; the host stamps them (see Sequencer).
type Event struct {
	Seq          uint64          `json:"seq"`
	RunID        string          `json:"runId"`
	TS           time.Time       `json:"ts"`
	Kind         Kind            `json:"kind"`
	ParentToolID string          `json:"parentToolId,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
}

// NewEvent builds an unstamped event with a JSON payload.
func NewEvent(kind Kind, parentToolID string, payload any) Event {
	e := Event{Kind: kind, ParentToolID: parentToolID}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			// Payloads are plain structs; this cannot happen in practice.
			b, _ = json.Marshal(ErrorData{Code: ErrProtocol, Message: err.Error()})
			e.Kind = KindError
		}
		e.Data = b
	}
	return e
}

// Decode unmarshals an event payload.
func Decode[T any](e Event) (T, error) {
	var v T
	if len(e.Data) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(e.Data, &v); err != nil {
		return v, fmt.Errorf("decode %s: %w", e.Kind, err)
	}
	return v, nil
}

// MustDecode is Decode that returns the zero value on error.
func MustDecode[T any](e Event) T {
	v, _ := Decode[T](e)
	return v
}

// Sequencer stamps Seq (starting at FirstSeq, default 1, gap-free), RunID and
// TS onto events. It is safe for concurrent use.
type Sequencer struct {
	RunID    string
	Now      func() time.Time
	FirstSeq uint64

	mu   sync.Mutex
	next uint64
}

// Stamp stamps events in place and returns them.
func (s *Sequencer) Stamp(evs []Event) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next == 0 {
		s.next = s.FirstSeq
		if s.next == 0 {
			s.next = 1
		}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	for i := range evs {
		evs[i].Seq = s.next
		s.next++
		evs[i].RunID = s.RunID
		evs[i].TS = now()
	}
	return evs
}

// ---- payloads ----

// RunStarted is emitted by the host when a process is spawned.
type RunStarted struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	ResumeID string `json:"resumeId,omitempty"`
}

// UserMessageData records a user turn.
type UserMessageData struct {
	Text   string     `json:"text"`
	Images []ImageRef `json:"images,omitempty"`
}

// ImageRef describes an attachment without its bytes.
type ImageRef struct {
	MediaType string `json:"mediaType"`
	Name      string `json:"name,omitempty"`
	Bytes     int    `json:"bytes"`
}

// NewUserMessageEvent builds the user_message event for m.
func NewUserMessageEvent(m UserMessage) Event {
	d := UserMessageData{Text: m.Text}
	for _, im := range m.Images {
		d.Images = append(d.Images, ImageRef{MediaType: im.MediaType, Name: im.Name, Bytes: len(im.Data)})
	}
	return NewEvent(KindUserMessage, "", d)
}

// MCPServer is an MCP server status from the init message.
type MCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
}

// SessionInit is the provider handshake result. Claude emits it every turn
// (deduplicated by the Session).
type SessionInit struct {
	ProviderSessionID string      `json:"providerSessionId"`
	Model             string      `json:"model,omitempty"`
	Mode              string      `json:"mode,omitempty"`
	Cwd               string      `json:"cwd,omitempty"`
	Version           string      `json:"version,omitempty"`
	Tools             []string    `json:"tools,omitempty"`
	MCPServers        []MCPServer `json:"mcpServers,omitempty"`
	SlashCommands     []string    `json:"slashCommands,omitempty"`
}

// BlockDelta is a streaming text/thinking fragment. MessageID+Index address
// the content block.
type BlockDelta struct {
	MessageID string `json:"messageId,omitempty"`
	Index     int    `json:"index"`
	Text      string `json:"text"`
}

// MessageDone finishes a text or thinking block with its final text.
type MessageDone struct {
	MessageID string `json:"messageId,omitempty"`
	Index     int    `json:"index"`
	BlockType string `json:"blockType"` // "text" | "thinking"
	Text      string `json:"text"`
}

// ToolStart announces a tool call.
type ToolStart struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ToolInputDelta is a partial-JSON fragment of a tool input.
type ToolInputDelta struct {
	ID          string `json:"id"`
	PartialJSON string `json:"partialJson"`
}

// ToolInputDone carries the complete tool input.
type ToolInputDone struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is a finished tool call.
type ToolResult struct {
	ID          string `json:"id"`
	OK          bool   `json:"ok"`
	Output      string `json:"output,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Diff        string `json:"diff,omitempty"` // unified diff
	ExitCode    *int   `json:"exitCode,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	Denied      bool   `json:"denied,omitempty"`
}

// Option kinds (ACP names).
const (
	OptAllowOnce   = "allow_once"
	OptAllowAlways = "allow_always"
	OptRejectOnce  = "reject_once"
)

// PermissionOption is one choice offered for a permission request.
type PermissionOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// PermissionRequest asks the user to approve a tool call.
type PermissionRequest struct {
	ReqID          string             `json:"reqId"`
	ToolUseID      string             `json:"toolUseId,omitempty"`
	Tool           string             `json:"tool"`
	DisplayName    string             `json:"displayName,omitempty"`
	Input          json.RawMessage    `json:"input,omitempty"`
	Description    string             `json:"description,omitempty"`
	DecisionReason string             `json:"decisionReason,omitempty"`
	Options        []PermissionOption `json:"options,omitempty"`
}

// PermissionResolved reports the answer given for ReqID.
type PermissionResolved struct {
	ReqID     string   `json:"reqId"`
	ToolUseID string   `json:"toolUseId,omitempty"`
	Behavior  Behavior `json:"behavior"`
	Message   string   `json:"message,omitempty"`
}

// PermissionCancelled reports that the provider withdrew ReqID.
type PermissionCancelled struct {
	ReqID     string `json:"reqId"`
	ToolUseID string `json:"toolUseId,omitempty"`
}

// PlanProposal is a plan awaiting approval (Claude ExitPlanMode, Cursor
// create_plan). Answer with RespondPermission(ReqID, ...).
type PlanProposal struct {
	ReqID     string `json:"reqId"`
	ToolUseID string `json:"toolUseId,omitempty"`
	Text      string `json:"text"`
	FilePath  string `json:"filePath,omitempty"`
}

// TodoItem is one checklist entry.
type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // pending | in_progress | completed
	ActiveForm string `json:"activeForm,omitempty"`
}

// TodoUpdate replaces the whole todo list.
type TodoUpdate struct {
	Todos []TodoItem `json:"todos"`
}

// Usage is PER TURN. CostUSD is the delta of the provider's cumulative cost.
type Usage struct {
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	CostUSD             float64 `json:"costUsd"`
	TotalCostUSD        float64 `json:"totalCostUsd"`
	Model               string  `json:"model,omitempty"`
	ContextWindow       int     `json:"contextWindow,omitempty"`
}

// Status is a transient run state.
type Status struct {
	State      string `json:"state"` // requesting | thinking | message_stop
	Tokens     int    `json:"tokens,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
}

// ModeChanged reports the active permission/agent mode.
type ModeChanged struct {
	Mode string `json:"mode"`
}

// ModelChanged reports a confirmed live model switch.
type ModelChanged struct {
	Model string `json:"model"`
}

// TaskUpdate reports a background task lifecycle step (Claude system/task_*).
type TaskUpdate struct {
	TaskID      string `json:"taskId"`
	ToolUseID   string `json:"toolUseId,omitempty"`
	Description string `json:"description,omitempty"`
	TaskType    string `json:"taskType,omitempty"`
	Status      string `json:"status"` // started | completed | ...
	Summary     string `json:"summary,omitempty"`
}

// ModelsList is the answer to a list_models request.
type ModelsList struct {
	Models []Model `json:"models"`
}

// ErrorData is a non-tool error.
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal,omitempty"`
}

// Run result statuses.
const (
	ResultSuccess     = "success"
	ResultInterrupted = "interrupted"
	ResultError       = "error"
)

// RunResult ends a turn.
type RunResult struct {
	ProviderSessionID string   `json:"providerSessionId,omitempty"`
	Status            string   `json:"status"`
	Subtype           string   `json:"subtype,omitempty"`
	Text              string   `json:"text,omitempty"`
	StopReason        string   `json:"stopReason,omitempty"`
	TerminalReason    string   `json:"terminalReason,omitempty"`
	NumTurns          int      `json:"numTurns,omitempty"`
	DurationMS        int64    `json:"durationMs,omitempty"`
	Errors            []string `json:"errors,omitempty"`
}
