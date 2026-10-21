package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sid/harness/internal/agent"
)

// maxOutput caps tool output carried in events (the full output stays in the
// CLI's own transcript).
const maxOutput = 100_000

var validModes = map[string]bool{
	"acceptEdits": true, "auto": true, "bypassPermissions": true,
	"default": true, "dontAsk": true, "plan": true,
}

// Session is the per-process Claude protocol state machine. It is safe for
// concurrent use: Feed runs on the reader goroutine while the action methods
// may be called from others.
type Session struct {
	w io.Writer

	mu sync.Mutex

	genID func() string
	idN   int

	sessionID string
	model     string
	resolved  string // resolved model name from the latest init
	mode      string
	lastInit  string

	curMsg     map[string]string // parent tool id -> current streaming message id
	asstCount  map[string]int    // message id -> assistant block lines seen
	blockTool  map[string]string // "msg:index" -> tool id
	blockKind  map[string]string // "msg:index" -> block type
	toolsSeen  map[string]*toolInfo
	pending    map[string]*pendingPerm
	pendingSeq []string
	ctl        map[string]ctlReq

	lastTotal float64
	resumeErr bool
}

type toolInfo struct {
	name    string
	input   json.RawMessage
	parent  string
	started bool
}

type pendingPerm struct {
	toolUseID string
	tool      string
	input     json.RawMessage
	parent    string
	plan      bool
}

type ctlReq struct {
	subtype string
	arg     string
}

var _ agent.Session = (*Session)(nil)

// Option configures a Session.
type Option func(*Session)

// WithRequestIDs overrides control request id generation (tests).
func WithRequestIDs(gen func() string) Option { return func(s *Session) { s.genID = gen } }

// NewSession creates a Session writing client messages to w (process stdin).
func NewSession(w io.Writer, o agent.StartOpts, opts ...Option) *Session {
	s := &Session{
		w:         w,
		model:     o.Model,
		mode:      o.Mode,
		curMsg:    map[string]string{},
		asstCount: map[string]int{},
		blockTool: map[string]string{},
		blockKind: map[string]string{},
		toolsSeen: map[string]*toolInfo{},
		pending:   map[string]*pendingPerm{},
		ctl:       map[string]ctlReq{},
	}
	if s.mode == "" {
		s.mode = "default"
	}
	for _, f := range opts {
		f(s)
	}
	return s
}

func (s *Session) newReqID() string {
	if s.genID != nil {
		return s.genID()
	}
	s.idN++
	return fmt.Sprintf("h-%d", s.idN)
}

func (s *Session) write(str string) error {
	_, err := io.WriteString(s.w, str)
	return err
}

// ---- agent.Session: actions ----

// Start is a no-op: the Claude CLI needs no handshake.
func (s *Session) Start(ctx context.Context) error { return nil }

// SendUser writes a user turn and returns the user_message event.
func (s *Session) SendUser(m agent.UserMessage) ([]agent.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(EncodeUser(m)); err != nil {
		return nil, err
	}
	return []agent.Event{agent.NewUserMessageEvent(m)}, nil
}

// RespondPermission answers a permission_request or plan_proposal.
func (s *Session) RespondPermission(reqID string, d agent.Decision) ([]agent.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[reqID]
	if !ok {
		return nil, fmt.Errorf("claude: no pending permission %q", reqID)
	}
	switch d.Behavior {
	case agent.BehaviorAllow, agent.BehaviorAllowAlways, agent.BehaviorDeny:
	default:
		return nil, fmt.Errorf("claude: invalid permission behavior %q", d.Behavior)
	}
	if p.plan && d.Behavior == agent.BehaviorAllowAlways {
		d.Behavior = agent.BehaviorAllow
	}
	if err := s.write(EncodePermission(reqID, d, p.tool, p.input)); err != nil {
		return nil, err
	}
	s.dropPending(reqID)
	msg := d.Message
	if d.Behavior == agent.BehaviorDeny && msg == "" {
		msg = DefaultDenyMessage
	}
	return []agent.Event{agent.NewEvent(agent.KindPermissionResolved, p.parent, agent.PermissionResolved{
		ReqID: reqID, ToolUseID: p.toolUseID, Behavior: d.Behavior, Message: msg,
	})}, nil
}

func (s *Session) dropPending(reqID string) {
	delete(s.pending, reqID)
	for i, id := range s.pendingSeq {
		if id == reqID {
			s.pendingSeq = append(s.pendingSeq[:i], s.pendingSeq[i+1:]...)
			break
		}
	}
}

func (s *Session) sendControl(subtype string, extra map[string]any, arg string) error {
	id := s.newReqID()
	s.ctl[id] = ctlReq{subtype: subtype, arg: arg}
	if err := s.write(EncodeControlRequest(id, subtype, extra)); err != nil {
		delete(s.ctl, id)
		return err
	}
	return nil
}

// Interrupt aborts the in-flight turn. The turn ends with a run_result of
// status "interrupted"; the process stays alive.
func (s *Session) Interrupt() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendControl("interrupt", nil, "")
}

// SetModel switches the model live; model_changed follows when the CLI
// acknowledges. An empty model resets to the default.
func (s *Session) SetModel(model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var extra map[string]any
	if model != "" {
		extra = map[string]any{"model": model}
	}
	return s.sendControl("set_model", extra, model)
}

// SetMode switches the permission mode live; mode_changed follows.
func (s *Session) SetMode(mode string) error {
	if !validModes[mode] {
		return fmt.Errorf("claude: invalid permission mode %q", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendControl("set_permission_mode", map[string]any{"mode": mode}, mode)
}

// ListModels requests the model list; a models_list event follows via Feed.
func (s *Session) ListModels() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendControl("list_models", nil, "")
}

// Closed reports process exit: pending permissions are cancelled and an
// abnormal exit becomes an error event.
func (s *Session) Closed(exitCode int, stderrTail string) []agent.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []agent.Event
	for _, id := range s.pendingSeq {
		p := s.pending[id]
		out = append(out, agent.NewEvent(agent.KindPermissionCancelled, p.parent,
			agent.PermissionCancelled{ReqID: id, ToolUseID: p.toolUseID}))
	}
	s.pending = map[string]*pendingPerm{}
	s.pendingSeq = nil
	if exitCode != 0 && !s.resumeErr {
		msg := fmt.Sprintf("claude exited with code %d", exitCode)
		if t := strings.TrimSpace(stderrTail); t != "" {
			msg += ": " + t
		}
		code := agent.ErrProcessExit
		if strings.Contains(stderrTail, "No conversation found") {
			code = agent.ErrResumeNotFound
		}
		out = append(out, agent.NewEvent(agent.KindError, "", agent.ErrorData{Code: code, Message: msg, Fatal: true}))
	}
	return out
}

// ---- Feed ----

type envelope struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	IsReplay        bool            `json:"isReplay"`
	APIMessageID    string          `json:"api_message_id"`
	Event           json.RawMessage `json:"event"`
	Message         json.RawMessage `json:"message"`
	RequestID       string          `json:"request_id"`
	Request         json.RawMessage `json:"request"`
	Response        json.RawMessage `json:"response"`
	ToolUseResult   json.RawMessage `json:"tool_use_result"`
	ToolResultMeta  json.RawMessage `json:"tool_result_meta"`
}

// Feed parses one stdout line.
func (s *Session) Feed(raw []byte) ([]agent.Event, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("claude: bad stdout line: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch env.Type {
	case "system":
		return s.onSystem(raw, env)
	case "stream_event":
		return s.onStream(env)
	case "assistant":
		return s.onAssistant(env)
	case "user":
		return s.onUser(env)
	case "control_request":
		return s.onControlRequest(env)
	case "control_cancel_request":
		return s.onCancel(env)
	case "control_response":
		return s.onControlResponse(env)
	case "result":
		return s.onResult(raw, env)
	}
	return nil, nil // rate_limit_event and unknown types are noise
}

func (s *Session) setMode(mode, parent string) []agent.Event {
	if mode == "" || mode == s.mode {
		return nil
	}
	s.mode = mode
	return []agent.Event{agent.NewEvent(agent.KindModeChanged, parent, agent.ModeChanged{Mode: mode})}
}

func (s *Session) onSystem(raw []byte, env envelope) ([]agent.Event, error) {
	switch env.Subtype {
	case "init":
		var in struct {
			SessionID      string   `json:"session_id"`
			Cwd            string   `json:"cwd"`
			Model          string   `json:"model"`
			PermissionMode string   `json:"permissionMode"`
			Version        string   `json:"claude_code_version"`
			Tools          []string `json:"tools"`
			SlashCommands  []string `json:"slash_commands"`
			MCPServers     []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"mcp_servers"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		s.sessionID = in.SessionID
		if in.Model != "" {
			s.resolved = in.Model
		}
		si := agent.SessionInit{
			ProviderSessionID: in.SessionID, Model: in.Model, Mode: in.PermissionMode, Cwd: in.Cwd,
			Version: in.Version, Tools: in.Tools, SlashCommands: in.SlashCommands,
		}
		for _, m := range in.MCPServers {
			si.MCPServers = append(si.MCPServers, agent.MCPServer{Name: m.Name, Status: m.Status})
		}
		var out []agent.Event
		b, _ := json.Marshal(si)
		if string(b) != s.lastInit {
			s.lastInit = string(b)
			out = append(out, agent.NewEvent(agent.KindSessionInit, "", si))
		}
		out = append(out, s.setMode(in.PermissionMode, "")...)
		return out, nil
	case "status":
		var st struct {
			Status         *string `json:"status"`
			PermissionMode string  `json:"permissionMode"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, err
		}
		var out []agent.Event
		if st.Status != nil && *st.Status != "" {
			out = append(out, agent.NewEvent(agent.KindStatus, "", agent.Status{State: *st.Status}))
		}
		return append(out, s.setMode(st.PermissionMode, "")...), nil
	case "thinking_tokens":
		var t struct {
			Estimated int `json:"estimated_tokens"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return []agent.Event{agent.NewEvent(agent.KindStatus, env.ParentToolUseID,
			agent.Status{State: "thinking", Tokens: t.Estimated})}, nil
	case "task_started", "task_notification", "task_progress", "task_updated":
		var t struct {
			TaskID      string `json:"task_id"`
			ToolUseID   string `json:"tool_use_id"`
			Description string `json:"description"`
			TaskType    string `json:"task_type"`
			Status      string `json:"status"`
			Summary     string `json:"summary"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		status := t.Status
		if env.Subtype == "task_started" {
			status = "started"
		}
		if status == "" {
			status = strings.TrimPrefix(env.Subtype, "task_")
		}
		return []agent.Event{agent.NewEvent(agent.KindTaskUpdate, env.ParentToolUseID, agent.TaskUpdate{
			TaskID: t.TaskID, ToolUseID: t.ToolUseID, Description: t.Description,
			TaskType: t.TaskType, Status: status, Summary: t.Summary,
		})}, nil
	}
	return nil, nil
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

func bkey(msg string, idx int) string { return fmt.Sprintf("%s:%d", msg, idx) }

func (s *Session) onStream(env envelope) ([]agent.Event, error) {
	var ev streamEvent
	if err := json.Unmarshal(env.Event, &ev); err != nil {
		return nil, err
	}
	parent := env.ParentToolUseID
	msg := env.APIMessageID
	if msg == "" {
		msg = s.curMsg[parent]
	}
	switch ev.Type {
	case "message_start":
		s.curMsg[parent] = ev.Message.ID
	case "content_block_start":
		s.blockKind[bkey(msg, ev.Index)] = ev.ContentBlock.Type
		if ev.ContentBlock.Type == "tool_use" {
			id := ev.ContentBlock.ID
			s.blockTool[bkey(msg, ev.Index)] = id
			ti := s.tool(id)
			ti.name, ti.parent, ti.started = ev.ContentBlock.Name, parent, true
			return []agent.Event{agent.NewEvent(agent.KindToolStart, parent,
				agent.ToolStart{ID: id, Name: ev.ContentBlock.Name})}, nil
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text == "" {
				return nil, nil
			}
			return []agent.Event{agent.NewEvent(agent.KindTextDelta, parent,
				agent.BlockDelta{MessageID: msg, Index: ev.Index, Text: ev.Delta.Text})}, nil
		case "thinking_delta":
			if ev.Delta.Thinking == "" {
				return nil, nil
			}
			return []agent.Event{agent.NewEvent(agent.KindThinkingDelta, parent,
				agent.BlockDelta{MessageID: msg, Index: ev.Index, Text: ev.Delta.Thinking})}, nil
		case "input_json_delta":
			id := s.blockTool[bkey(msg, ev.Index)]
			if id == "" || ev.Delta.PartialJSON == "" {
				return nil, nil
			}
			return []agent.Event{agent.NewEvent(agent.KindToolInputDelta, parent,
				agent.ToolInputDelta{ID: id, PartialJSON: ev.Delta.PartialJSON})}, nil
		}
	case "message_stop":
		delete(s.asstCount, msg)
		prefix := msg + ":"
		for k := range s.blockTool {
			if strings.HasPrefix(k, prefix) {
				delete(s.blockTool, k)
			}
		}
		for k := range s.blockKind {
			if strings.HasPrefix(k, prefix) {
				delete(s.blockKind, k)
			}
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			return []agent.Event{agent.NewEvent(agent.KindStatus, parent,
				agent.Status{State: "message_stop", StopReason: ev.Delta.StopReason})}, nil
		}
	}
	return nil, nil
}

func (s *Session) tool(id string) *toolInfo {
	ti := s.toolsSeen[id]
	if ti == nil {
		ti = &toolInfo{}
		s.toolsSeen[id] = ti
	}
	return ti
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type messageBody struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

func (s *Session) onAssistant(env envelope) ([]agent.Event, error) {
	var m messageBody
	if err := json.Unmarshal(env.Message, &m); err != nil {
		return nil, err
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, nil // string content: nothing structured to emit
	}
	parent := env.ParentToolUseID
	var out []agent.Event
	for _, b := range blocks {
		idx := s.asstCount[m.ID]
		s.asstCount[m.ID]++
		switch b.Type {
		case "text":
			out = append(out, agent.NewEvent(agent.KindMessageDone, parent,
				agent.MessageDone{MessageID: m.ID, Index: idx, BlockType: "text", Text: b.Text}))
		case "thinking":
			out = append(out, agent.NewEvent(agent.KindMessageDone, parent,
				agent.MessageDone{MessageID: m.ID, Index: idx, BlockType: "thinking", Text: b.Thinking}))
		case "tool_use":
			ti := s.tool(b.ID)
			if !ti.started {
				ti.started = true
				out = append(out, agent.NewEvent(agent.KindToolStart, parent, agent.ToolStart{ID: b.ID, Name: b.Name}))
			}
			ti.name, ti.input, ti.parent = b.Name, b.Input, parent
			out = append(out, agent.NewEvent(agent.KindToolInputDone, parent,
				agent.ToolInputDone{ID: b.ID, Name: b.Name, Input: b.Input}))
			if b.Name == "TodoWrite" {
				var in struct {
					Todos []agent.TodoItem `json:"todos"`
				}
				if json.Unmarshal(b.Input, &in) == nil {
					out = append(out, agent.NewEvent(agent.KindTodoUpdate, parent, agent.TodoUpdate{Todos: in.Todos}))
				}
			}
		}
	}
	return out, nil
}

func (s *Session) onUser(env envelope) ([]agent.Event, error) {
	if env.IsReplay {
		return nil, nil // echo of our own turn (SendUser already emitted user_message)
	}
	var m messageBody
	if err := json.Unmarshal(env.Message, &m); err != nil {
		return nil, err
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil, nil
	}
	var out []agent.Event
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue // e.g. "[Request interrupted by user]"
		}
		out = append(out, s.toolResult(env, b))
	}
	return out, nil
}

func (s *Session) toolResult(env envelope, b contentBlock) agent.Event {
	ti := s.toolsSeen[b.ToolUseID]
	parent := env.ParentToolUseID
	name := ""
	var input json.RawMessage
	if ti != nil {
		name, input = ti.name, ti.input
		if parent == "" {
			parent = ti.parent
		}
	}
	out, trunc := truncate(contentText(b.Content))
	res := agent.ToolResult{ID: b.ToolUseID, OK: !b.IsError, Output: out, Truncated: trunc}

	var meta []struct {
		PermissionDecision struct {
			Decision string `json:"decision"`
		} `json:"permission_decision"`
	}
	if json.Unmarshal(env.ToolResultMeta, &meta) == nil {
		for _, mm := range meta {
			if mm.PermissionDecision.Decision == "reject" {
				res.Denied = true
			}
		}
	}
	var tur struct {
		Interrupted     bool            `json:"interrupted"`
		ExitCode        *int            `json:"exitCode"`
		ExitCodeSnake   *int            `json:"exit_code"`
		ReturnCode      *int            `json:"returnCode"`
		Type            string          `json:"type"`
		FilePath        string          `json:"filePath"`
		Content         string          `json:"content"`
		StructuredPatch json.RawMessage `json:"structuredPatch"`
	}
	isObj := len(env.ToolUseResult) > 0 && env.ToolUseResult[0] == '{' && json.Unmarshal(env.ToolUseResult, &tur) == nil
	if isObj {
		res.Interrupted = tur.Interrupted
		switch {
		case tur.ExitCode != nil:
			res.ExitCode = tur.ExitCode
		case tur.ExitCodeSnake != nil:
			res.ExitCode = tur.ExitCodeSnake
		case tur.ReturnCode != nil:
			res.ExitCode = tur.ReturnCode
		}
	}
	if !b.IsError && (name == "Edit" || name == "Write" || name == "MultiEdit") {
		path := tur.FilePath
		if path == "" {
			var in struct {
				FilePath string `json:"file_path"`
			}
			_ = json.Unmarshal(input, &in)
			path = in.FilePath
		}
		if d, ok := agent.DiffFromStructuredPatch(path, tur.StructuredPatch); isObj && ok {
			res.Diff = d
		} else if isObj && tur.Type == "create" {
			res.Diff = agent.DiffNewFile(path, tur.Content)
		} else {
			res.Diff = agent.DiffFromToolInput(name, input)
		}
	}
	delete(s.toolsSeen, b.ToolUseID)
	return agent.NewEvent(agent.KindToolResult, parent, res)
}

func truncate(s string) (string, bool) {
	if len(s) <= maxOutput {
		return s, false
	}
	s = s[:maxOutput]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s, true
}

// contentText flattens a tool_result content (string or block array).
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

type canUseTool struct {
	Subtype             string          `json:"subtype"`
	ToolName            string          `json:"tool_name"`
	DisplayName         string          `json:"display_name"`
	Input               json.RawMessage `json:"input"`
	Description         string          `json:"description"`
	ToolUseID           string          `json:"tool_use_id"`
	DecisionReason      string          `json:"decision_reason"`
	RequiresInteraction bool            `json:"requires_user_interaction"`
}

func (s *Session) onControlRequest(env envelope) ([]agent.Event, error) {
	var r canUseTool
	if err := json.Unmarshal(env.Request, &r); err != nil {
		return nil, err
	}
	if r.Subtype != "can_use_tool" {
		// Never leave a CLI request unanswered: it would block the turn.
		return nil, s.write(EncodeControlError(env.RequestID, "unsupported control request: "+r.Subtype))
	}
	parent := ""
	if ti := s.toolsSeen[r.ToolUseID]; ti != nil {
		parent = ti.parent
	}
	p := &pendingPerm{toolUseID: r.ToolUseID, tool: r.ToolName, input: r.Input, parent: parent}
	if _, dup := s.pending[env.RequestID]; !dup {
		s.pendingSeq = append(s.pendingSeq, env.RequestID)
	}
	s.pending[env.RequestID] = p
	if r.ToolName == "ExitPlanMode" {
		p.plan = true
		var in struct {
			Plan         string `json:"plan"`
			PlanFilePath string `json:"planFilePath"`
		}
		_ = json.Unmarshal(r.Input, &in)
		return []agent.Event{agent.NewEvent(agent.KindPlanProposal, parent, agent.PlanProposal{
			ReqID: env.RequestID, ToolUseID: r.ToolUseID, Text: in.Plan, FilePath: in.PlanFilePath,
		})}, nil
	}
	return []agent.Event{agent.NewEvent(agent.KindPermissionRequest, parent, agent.PermissionRequest{
		ReqID: env.RequestID, ToolUseID: r.ToolUseID, Tool: r.ToolName, DisplayName: r.DisplayName,
		Input: r.Input, Description: r.Description, DecisionReason: r.DecisionReason,
		Options: []agent.PermissionOption{
			{ID: "allow", Name: "Allow", Kind: agent.OptAllowOnce},
			{ID: "allow_always", Name: "Always allow", Kind: agent.OptAllowAlways},
			{ID: "deny", Name: "Deny", Kind: agent.OptRejectOnce},
		},
	})}, nil
}

func (s *Session) onCancel(env envelope) ([]agent.Event, error) {
	p, ok := s.pending[env.RequestID]
	if !ok {
		return nil, nil
	}
	s.dropPending(env.RequestID)
	return []agent.Event{agent.NewEvent(agent.KindPermissionCancelled, p.parent,
		agent.PermissionCancelled{ReqID: env.RequestID, ToolUseID: p.toolUseID})}, nil
}

func (s *Session) onControlResponse(env envelope) ([]agent.Event, error) {
	var r struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Error     string          `json:"error"`
		ErrorCode string          `json:"error_code"`
		Response  json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(env.Response, &r); err != nil {
		return nil, err
	}
	req, ok := s.ctl[r.RequestID]
	if !ok {
		return nil, nil
	}
	delete(s.ctl, r.RequestID)
	if r.Subtype == "error" {
		code := r.ErrorCode
		if code == "" {
			code = agent.ErrControl
		}
		return []agent.Event{agent.NewEvent(agent.KindError, "", agent.ErrorData{
			Code: code, Message: fmt.Sprintf("%s: %s", req.subtype, r.Error),
		})}, nil
	}
	switch req.subtype {
	case "set_model":
		s.model = req.arg
		return []agent.Event{agent.NewEvent(agent.KindModelChanged, "", agent.ModelChanged{Model: req.arg})}, nil
	case "set_permission_mode":
		var body struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(r.Response, &body)
		if body.Mode == "" {
			body.Mode = req.arg
		}
		return s.setMode(body.Mode, ""), nil
	case "list_models":
		var body struct {
			Models []modelInfo `json:"models"`
		}
		if err := json.Unmarshal(r.Response, &body); err != nil {
			return nil, err
		}
		return []agent.Event{agent.NewEvent(agent.KindModelsList, "", agent.ModelsList{Models: convertModels(body.Models)})}, nil
	}
	return nil, nil
}

type resultMsg struct {
	Subtype        string   `json:"subtype"`
	IsError        bool     `json:"is_error"`
	Result         string   `json:"result"`
	SessionID      string   `json:"session_id"`
	NumTurns       int      `json:"num_turns"`
	StopReason     string   `json:"stop_reason"`
	TerminalReason string   `json:"terminal_reason"`
	DurationMS     int64    `json:"duration_ms"`
	TotalCostUSD   float64  `json:"total_cost_usd"`
	Errors         []string `json:"errors"`
	Usage          struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		ContextWindow int `json:"contextWindow"`
	} `json:"modelUsage"`
}

func round9(f float64) float64 { return math.Round(f*1e9) / 1e9 }

func (s *Session) onResult(raw []byte, env envelope) ([]agent.Event, error) {
	var r resultMsg
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var out []agent.Event

	status := agent.ResultSuccess
	switch {
	case r.TerminalReason == "aborted_streaming":
		status = agent.ResultInterrupted
	case r.IsError || r.Subtype != "success":
		status = agent.ResultError
	}
	notFound := false
	for _, e := range r.Errors {
		if strings.Contains(e, "No conversation found") {
			notFound = true
		}
	}

	if !notFound {
		// total_cost_usd is cumulative per process: the per-turn cost is the
		// difference. An aborted turn reports 0 and must not reset the base;
		// a decrease means the counter restarted.
		var cost float64
		switch {
		case r.TotalCostUSD == 0 && status != agent.ResultSuccess:
		case r.TotalCostUSD >= s.lastTotal:
			cost = r.TotalCostUSD - s.lastTotal
			s.lastTotal = r.TotalCostUSD
		default:
			cost = r.TotalCostUSD
			s.lastTotal = r.TotalCostUSD
		}
		u := agent.Usage{
			InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens,
			CacheReadTokens: r.Usage.CacheReadTokens, CacheCreationTokens: r.Usage.CacheCreationTokens,
			CostUSD: round9(cost), TotalCostUSD: round9(r.TotalCostUSD),
		}
		models := make([]string, 0, len(r.ModelUsage))
		for k := range r.ModelUsage {
			models = append(models, k)
		}
		sort.Strings(models)
		if _, ok := r.ModelUsage[s.resolved]; ok {
			models = []string{s.resolved}
		}
		if len(models) > 0 {
			u.Model = models[0]
			u.ContextWindow = r.ModelUsage[models[0]].ContextWindow
		}
		out = append(out, agent.NewEvent(agent.KindUsage, "", u))
	}

	switch {
	case notFound:
		s.resumeErr = true
		out = append(out, agent.NewEvent(agent.KindError, "", agent.ErrorData{
			Code: agent.ErrResumeNotFound, Message: strings.Join(r.Errors, "; "), Fatal: true,
		}))
	case status == agent.ResultError:
		msg := strings.Join(r.Errors, "; ")
		if msg == "" {
			msg = "run failed: " + r.Subtype
		}
		out = append(out, agent.NewEvent(agent.KindError, "", agent.ErrorData{Code: agent.ErrRunFailed, Message: msg}))
	}

	rr := agent.RunResult{
		ProviderSessionID: r.SessionID, Status: status, Subtype: r.Subtype, Text: r.Result,
		StopReason: r.StopReason, TerminalReason: r.TerminalReason, NumTurns: r.NumTurns, DurationMS: r.DurationMS,
	}
	if status != agent.ResultInterrupted {
		rr.Errors = r.Errors
	}
	out = append(out, agent.NewEvent(agent.KindRunResult, "", rr))
	return out, nil
}
