package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/sid/harness/internal/agent"
)

// Config tunes a generic ACP Session for one agent (see internal/agent/cursor).
type Config struct {
	// ProviderID is stamped on listed models ("cursor").
	ProviderID string
	// AuthMethodID is the preferred authenticate method. When the agent does
	// not offer it the first offered method is used; with no authMethods the
	// authenticate step is skipped.
	AuthMethodID string
	// ClientName/ClientVersion go in initialize.clientInfo.
	ClientName, ClientVersion string
	// DisableFS / DisableTerminal stop advertising (and serving) the fs/* and
	// terminal/* client capabilities. Cursor never uses them.
	DisableFS, DisableTerminal bool
	// OnModels is called (on the Feed goroutine) with the model list from each
	// session/new or session/load response.
	OnModels func([]agent.Model)
	// Now is the clock (tests); default time.Now.
	Now func() time.Time
}

// Session is the ACP protocol state machine for one spawned agent process.
// It is safe for concurrent use; see the package doc for the Start/Feed
// concurrency design.
type Session struct {
	cfg  Config
	opts agent.StartOpts
	cwd  string
	c    *conn

	terms termTable

	mu         sync.Mutex
	sessionID  string
	ready      bool
	curModel   string
	curMode    string
	models     []agent.Model
	commands   []string
	turn       int
	blockIdx   int
	curKind    string // "text" | "thinking" | "" (open streaming block)
	curText    strings.Builder
	lastText   string
	userBuf    strings.Builder
	tools      map[string]*toolState
	rejected   map[string]bool
	reqs       map[string]*agentReq
	reqOrder   []string
	todos      []todoEntry
	promptLive bool
	promptAt   time.Time
}

type toolState struct {
	id        string
	name      string
	title     string
	raw       map[string]any
	locs      []location
	lastInput []byte
	diff      string
	done      bool
}

type agentReq struct {
	kind       string // "perm" | "plan"
	id         json.RawMessage
	toolCallID string
	options    []agent.PermissionOption
	answered   bool
}

var _ agent.Session = (*Session)(nil)

// NewSession creates a session writing NDJSON requests to w (the agent's
// stdin). Create one per spawned process.
func NewSession(w io.Writer, o agent.StartOpts, cfg Config) *Session {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "harness"
	}
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = "0.1.0"
	}
	return &Session{
		cfg: cfg, opts: o, cwd: o.Cwd, c: newConn(w),
		tools: map[string]*toolState{}, rejected: map[string]bool{}, reqs: map[string]*agentReq{},
	}
}

func ev(kind agent.Kind, payload any) agent.Event { return agent.NewEvent(kind, "", payload) }

// ---- handshake ----

func (s *Session) await(ctx context.Context, method string, params any,
	on func(json.RawMessage, *RPCError) []agent.Event) (json.RawMessage, error) {
	cl, err := s.c.request(method, params, on)
	if err != nil {
		return nil, err
	}
	select {
	case r := <-cl.ch:
		return r.Result, r.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Start runs initialize, authenticate and session/new|load, then applies the
// requested model and mode. It blocks until each response has been delivered
// through Feed (see the package doc: no deadlock is possible).
func (s *Session) Start(ctx context.Context) error {
	caps := map[string]any{
		"fs":       map[string]any{"readTextFile": !s.cfg.DisableFS, "writeTextFile": !s.cfg.DisableFS},
		"terminal": !s.cfg.DisableTerminal,
	}
	res, err := s.await(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": caps,
		"clientInfo":         map[string]any{"name": s.cfg.ClientName, "version": s.cfg.ClientVersion},
	}, nil)
	if err != nil {
		return fmt.Errorf("acp initialize: %w", err)
	}
	var ir struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	_ = json.Unmarshal(res, &ir)
	if len(ir.AuthMethods) > 0 {
		method := ir.AuthMethods[0].ID
		for _, m := range ir.AuthMethods {
			if m.ID == s.cfg.AuthMethodID {
				method = m.ID
			}
		}
		if _, err := s.await(ctx, "authenticate", map[string]any{"methodId": method}, nil); err != nil {
			return fmt.Errorf("acp authenticate (%s; try `agent login`): %w", method, err)
		}
	}
	if s.opts.ResumeID != "" {
		s.mu.Lock()
		s.sessionID = s.opts.ResumeID
		s.mu.Unlock()
		_, err = s.await(ctx, "session/load", map[string]any{
			"sessionId": s.opts.ResumeID, "cwd": s.cwd, "mcpServers": []any{},
		}, s.onSetup(true))
	} else {
		_, err = s.await(ctx, "session/new", map[string]any{
			"cwd": s.cwd, "mcpServers": []any{},
		}, s.onSetup(false))
	}
	if err != nil {
		return fmt.Errorf("acp session setup: %w", err)
	}
	s.mu.Lock()
	cmode := s.curMode
	s.mu.Unlock()
	// The model is always set explicitly: the agent's own default can change
	// between runs, and set_model is idempotent.
	if m := s.opts.Model; m != "" {
		cl, err := s.sendSetModel(m)
		if err != nil {
			return err
		}
		select {
		case r := <-cl.ch:
			if r.Err != nil {
				return fmt.Errorf("acp set_model %q: %w", m, r.Err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m := s.opts.Mode; m != "" && m != cmode {
		cl, err := s.sendSetMode(m)
		if err != nil {
			return err
		}
		select {
		case r := <-cl.ch:
			if r.Err != nil {
				return fmt.Errorf("acp set_mode %q: %w", m, r.Err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type modelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Session) onSetup(load bool) func(json.RawMessage, *RPCError) []agent.Event {
	return func(res json.RawMessage, rerr *RPCError) []agent.Event {
		s.mu.Lock()
		if rerr != nil {
			evs := s.flushUserLocked()
			code, msg := agent.ErrControl, "session/new failed: "+rerr.Error()
			if load {
				code, msg = agent.ErrResumeNotFound, "session/load failed: "+rerr.Error()
			}
			s.mu.Unlock()
			return append(evs, ev(agent.KindError, agent.ErrorData{Code: code, Message: msg, Fatal: true}))
		}
		var r struct {
			SessionID string `json:"sessionId"`
			Modes     struct {
				CurrentModeID string `json:"currentModeId"`
			} `json:"modes"`
			Models struct {
				CurrentModelID  string      `json:"currentModelId"`
				AvailableModels []modelInfo `json:"availableModels"`
			} `json:"models"`
		}
		_ = json.Unmarshal(res, &r)
		if !load && r.SessionID != "" {
			s.sessionID = r.SessionID
		}
		s.curMode, s.curModel = r.Modes.CurrentModeID, r.Models.CurrentModelID
		s.models = nil
		for _, m := range r.Models.AvailableModels {
			name := m.Name
			if name == "" {
				name = m.ModelID
			}
			s.models = append(s.models, agent.Model{ID: m.ModelID, Provider: s.cfg.ProviderID, Name: name, Description: m.Description})
		}
		s.ready = true
		evs := s.flushUserLocked()
		evs = append(evs, s.sessionInitLocked(), ev(agent.KindModelsList, agent.ModelsList{Models: s.models}))
		models := append([]agent.Model(nil), s.models...)
		s.mu.Unlock()
		if s.cfg.OnModels != nil && len(models) > 0 {
			s.cfg.OnModels(models)
		}
		return evs
	}
}

func (s *Session) sessionInitLocked() agent.Event {
	return ev(agent.KindSessionInit, agent.SessionInit{
		ProviderSessionID: s.sessionID, Model: s.curModel, Mode: s.curMode, Cwd: s.cwd,
		SlashCommands: s.commands,
	})
}

// ---- outgoing actions ----

func (s *Session) sid() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID == "" {
		return "", errors.New("acp: no session yet (Start has not completed)")
	}
	return s.sessionID, nil
}

// SendUser sends session/prompt. The stop reason arrives later via Feed as a
// run_result event.
func (s *Session) SendUser(m agent.UserMessage) ([]agent.Event, error) {
	sid, err := s.sid()
	if err != nil {
		return nil, err
	}
	blocks := []map[string]any{}
	if m.Text != "" || len(m.Images) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
	}
	for _, im := range m.Images {
		blocks = append(blocks, map[string]any{"type": "image", "mimeType": im.MediaType, "data": im.Data})
	}
	s.mu.Lock()
	if s.promptLive {
		s.mu.Unlock()
		return nil, errors.New("acp: a prompt is already running")
	}
	pre := s.flushUserLocked()
	pre = append(pre, s.closeBlockLocked()...)
	s.promptLive = true
	s.promptAt = s.cfg.Now()
	s.turn++
	s.blockIdx = 0
	s.lastText = ""
	s.mu.Unlock()
	_, err = s.c.request("session/prompt", map[string]any{"sessionId": sid, "prompt": blocks}, s.onPrompt)
	if err != nil {
		s.mu.Lock()
		s.promptLive = false
		s.mu.Unlock()
		return nil, err
	}
	return append(pre, agent.NewUserMessageEvent(m)), nil
}

func (s *Session) onPrompt(res json.RawMessage, rerr *RPCError) []agent.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.flushUserLocked()
	evs = append(evs, s.closeBlockLocked()...)
	rr := agent.RunResult{ProviderSessionID: s.sessionID, Text: s.lastText}
	if !s.promptAt.IsZero() {
		rr.DurationMS = s.cfg.Now().Sub(s.promptAt).Milliseconds()
	}
	if rerr != nil {
		rr.Status, rr.Subtype, rr.Errors = agent.ResultError, "error", []string{rerr.Error()}
		evs = append(evs, ev(agent.KindError, agent.ErrorData{Code: agent.ErrRunFailed, Message: rerr.Error()}))
	} else {
		var r struct {
			StopReason string `json:"stopReason"`
		}
		_ = json.Unmarshal(res, &r)
		rr.StopReason = r.StopReason
		switch r.StopReason {
		case "cancelled":
			rr.Status = agent.ResultInterrupted
		case "refusal", "max_tokens", "max_turn_requests":
			rr.Status, rr.Subtype = agent.ResultError, r.StopReason
			rr.Errors = []string{"stopped: " + r.StopReason}
		default:
			rr.Status = agent.ResultSuccess
		}
		rr.Subtype = r.StopReason
	}
	// Requests still open when the turn ends can no longer be answered
	// usefully: withdraw them.
	for _, id := range s.reqOrder {
		q := s.reqs[id]
		if !q.answered {
			s.writeCancelledLocked(q)
		}
		evs = append(evs, ev(agent.KindPermissionCancelled, agent.PermissionCancelled{ReqID: id, ToolUseID: q.toolCallID}))
	}
	s.reqs, s.reqOrder = map[string]*agentReq{}, nil
	s.promptLive = false
	return append(evs, ev(agent.KindRunResult, rr))
}

// writeCancelledLocked answers a permission/plan request with the ACP
// "cancelled" outcome. Only used when a turn is cancelled or ended.
func (s *Session) writeCancelledLocked(q *agentReq) {
	q.answered = true
	_ = s.c.reply(q.id, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
}

// Interrupt sends session/cancel. Open permission requests are answered with
// the cancelled outcome as ACP requires; the matching permission_cancelled
// events are emitted with the turn's run_result.
func (s *Session) Interrupt() error {
	sid, err := s.sid()
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, id := range s.reqOrder {
		if q := s.reqs[id]; !q.answered {
			s.writeCancelledLocked(q)
		}
	}
	s.mu.Unlock()
	return s.c.notify("session/cancel", map[string]any{"sessionId": sid})
}

func (s *Session) sendSetModel(model string) (*call, error) {
	sid, err := s.sid()
	if err != nil {
		return nil, err
	}
	return s.c.request("session/set_model", map[string]any{"sessionId": sid, "modelId": model},
		func(_ json.RawMessage, rerr *RPCError) []agent.Event {
			if rerr != nil {
				return []agent.Event{ev(agent.KindError, agent.ErrorData{Code: agent.ErrControl,
					Message: fmt.Sprintf("set_model %q: %s", model, rerr.Error())})}
			}
			s.mu.Lock()
			s.curModel = model
			s.mu.Unlock()
			return []agent.Event{ev(agent.KindModelChanged, agent.ModelChanged{Model: model})}
		})
}

func (s *Session) sendSetMode(mode string) (*call, error) {
	sid, err := s.sid()
	if err != nil {
		return nil, err
	}
	return s.c.request("session/set_mode", map[string]any{"sessionId": sid, "modeId": mode},
		func(_ json.RawMessage, rerr *RPCError) []agent.Event {
			if rerr != nil {
				return []agent.Event{ev(agent.KindError, agent.ErrorData{Code: agent.ErrControl,
					Message: fmt.Sprintf("set_mode %q: %s", mode, rerr.Error())})}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.curMode == mode {
				return nil // current_mode_update already reported it
			}
			s.curMode = mode
			return []agent.Event{ev(agent.KindModeChanged, agent.ModeChanged{Mode: mode})}
		})
}

// SetModel sends session/set_model; model_changed follows via Feed.
func (s *Session) SetModel(model string) error { _, err := s.sendSetModel(model); return err }

// SetMode sends session/set_mode; mode_changed follows via Feed.
func (s *Session) SetMode(mode string) error { _, err := s.sendSetMode(mode); return err }

// RespondPermission answers a permission_request or plan_proposal.
//
// Permission decisions map to the agent's offered options: allow to the
// allow_once option (allow-once), allow_always to allow_always, deny to
// reject_once (reject-once). The cancelled outcome is never used for a deny:
// Cursor treats it as a reject and retries the tool. Plans answer
// accepted / rejected.
func (s *Session) RespondPermission(reqID string, d agent.Decision) ([]agent.Event, error) {
	s.mu.Lock()
	q, ok := s.reqs[reqID]
	if !ok || q.answered {
		s.mu.Unlock()
		return nil, fmt.Errorf("acp: no pending request %q", reqID)
	}
	switch d.Behavior {
	case agent.BehaviorAllow, agent.BehaviorAllowAlways, agent.BehaviorDeny:
	default:
		s.mu.Unlock()
		return nil, fmt.Errorf("acp: invalid permission behavior %q", d.Behavior)
	}
	var result map[string]any
	if q.kind == "plan" {
		out := "accepted"
		if d.Behavior == agent.BehaviorDeny {
			out = "rejected"
		}
		result = map[string]any{"outcome": map[string]any{"outcome": out}}
		if d.Behavior == agent.BehaviorAllowAlways {
			d.Behavior = agent.BehaviorAllow
		}
	} else {
		result = PermissionOutcome(q.options, d)
	}
	q.answered = true
	delete(s.reqs, reqID)
	for i, id := range s.reqOrder {
		if id == reqID {
			s.reqOrder = append(s.reqOrder[:i], s.reqOrder[i+1:]...)
			break
		}
	}
	if d.Behavior == agent.BehaviorDeny {
		s.rejected[q.toolCallID] = true
	}
	s.mu.Unlock()
	if err := s.c.reply(q.id, result); err != nil {
		return nil, err
	}
	msg := d.Message
	return []agent.Event{ev(agent.KindPermissionResolved, agent.PermissionResolved{
		ReqID: reqID, ToolUseID: q.toolCallID, Behavior: d.Behavior, Message: msg,
	})}, nil
}

// PermissionOutcome maps a Decision to a session/request_permission result.
func PermissionOutcome(opts []agent.PermissionOption, d agent.Decision) map[string]any {
	sel := func(id string) map[string]any {
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": id}}
	}
	if d.OptionID != "" {
		return sel(d.OptionID)
	}
	find := func(kind, fallback string) string {
		for _, o := range opts {
			if o.Kind == kind {
				return o.ID
			}
		}
		return fallback
	}
	switch d.Behavior {
	case agent.BehaviorAllowAlways:
		return sel(find(agent.OptAllowAlways, find(agent.OptAllowOnce, "allow-once")))
	case agent.BehaviorDeny:
		return sel(find(agent.OptRejectOnce, find("reject_always", "reject-once")))
	}
	return sel(find(agent.OptAllowOnce, "allow-once"))
}

// Closed is called when the process exited.
func (s *Session) Closed(exitCode int, stderrTail string) []agent.Event {
	s.c.failAll(errors.New("agent process exited"))
	s.killTerminals()
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.flushUserLocked()
	evs = append(evs, s.closeBlockLocked()...)
	for _, id := range s.reqOrder {
		evs = append(evs, ev(agent.KindPermissionCancelled, agent.PermissionCancelled{ReqID: id, ToolUseID: s.reqs[id].toolCallID}))
	}
	s.reqs, s.reqOrder = map[string]*agentReq{}, nil
	if exitCode != 0 {
		msg := fmt.Sprintf("agent exited with code %d", exitCode)
		if t := strings.TrimSpace(stderrTail); t != "" {
			msg += ": " + t
		}
		evs = append(evs, ev(agent.KindError, agent.ErrorData{Code: agent.ErrProcessExit, Message: msg, Fatal: true}))
		if s.promptLive {
			evs = append(evs, ev(agent.KindRunResult, agent.RunResult{
				ProviderSessionID: s.sessionID, Status: agent.ResultError, Errors: []string{msg},
			}))
		}
	}
	s.promptLive = false
	return evs
}

// ---- Feed ----

// Feed handles one NDJSON line from the agent.
func (s *Session) Feed(line []byte) ([]agent.Event, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, nil
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, fmt.Errorf("acp: bad line: %w", err)
	}
	switch {
	case m.isResponse():
		s.mu.Lock()
		pre := s.flushUserLocked()
		s.mu.Unlock()
		evs, ok := s.c.complete(&m)
		if !ok {
			return pre, nil
		}
		return append(pre, evs...), nil
	case m.isRequest():
		return s.onRequest(&m), nil
	case m.isNotify():
		if m.Method != "session/update" {
			return nil, nil // cursor/task, cursor/generate_image, ...
		}
		var p struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		var u update
		if json.Unmarshal(m.Params, &p) != nil || json.Unmarshal(p.Update, &u) != nil {
			return nil, errors.New("acp: bad session/update")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if p.SessionID != "" && s.sessionID != "" && p.SessionID != s.sessionID {
			return nil, nil
		}
		return s.onUpdateLocked(&u), nil
	}
	return nil, nil
}

func (s *Session) onRequest(m *message) []agent.Event {
	switch m.Method {
	case "session/request_permission":
		return s.onPermission(m)
	case "cursor/create_plan":
		return s.onCreatePlan(m)
	case "cursor/update_todos":
		return s.onUpdateTodos(m)
	case "cursor/ask_question":
		// Interactive questions are not supported yet: fail the request
		// instead of leaving the agent blocked.
		_ = s.c.replyErr(m.ID, CodeUnsupported, "cursor/ask_question is not supported by this client")
	case "cursor/task", "cursor/generate_image":
		_ = s.c.reply(m.ID, map[string]any{})
	case "fs/read_text_file", "fs/write_text_file":
		if s.cfg.DisableFS {
			_ = s.c.replyErr(m.ID, CodeMethodNotFound, "method not found: "+m.Method)
		} else {
			s.handleFS(m)
		}
	case "terminal/create", "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		switch {
		case s.cfg.DisableTerminal:
			_ = s.c.replyErr(m.ID, CodeMethodNotFound, "method not found: "+m.Method)
		case m.Method == "terminal/wait_for_exit":
			go s.handleTerminal(m) // blocks until exit; keep the reader free
		default:
			s.handleTerminal(m)
		}
	default:
		_ = s.c.replyErr(m.ID, CodeMethodNotFound, "method not found: "+m.Method)
	}
	return nil
}

func (s *Session) onPermission(m *message) []agent.Event {
	var p struct {
		ToolCall struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			Content    json.RawMessage `json:"content"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(m.Params, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.closeBlockLocked()
	name, input := "", json.RawMessage(nil)
	// The permission request carries no rawInput: reuse the announced call.
	if ts := s.tools[p.ToolCall.ToolCallID]; ts != nil {
		name, input = ts.name, ts.lastInput
	} else {
		name = toolName(p.ToolCall.Kind, nil)
		input = toolInput(name, p.ToolCall.Title, s.cwd, nil, nil)
	}
	var reason []string
	for _, c := range parseToolContent(p.ToolCall.Content) {
		if c.Content.Text != "" {
			reason = append(reason, c.Content.Text)
		}
	}
	q := &agentReq{kind: "perm", id: append(json.RawMessage(nil), m.ID...), toolCallID: p.ToolCall.ToolCallID}
	req := agent.PermissionRequest{
		ReqID: idKey(m.ID), ToolUseID: p.ToolCall.ToolCallID, Tool: name, Input: input,
		DecisionReason: strings.Join(reason, "\n"),
	}
	for _, o := range p.Options {
		po := agent.PermissionOption{ID: o.OptionID, Name: o.Name, Kind: o.Kind}
		req.Options = append(req.Options, po)
		q.options = append(q.options, po)
	}
	s.reqs[req.ReqID] = q
	s.reqOrder = append(s.reqOrder, req.ReqID)
	return append(evs, ev(agent.KindPermissionRequest, req))
}

func (s *Session) onCreatePlan(m *message) []agent.Event {
	var p struct {
		ToolCallID string `json:"toolCallId"`
		Name       string `json:"name"`
		Overview   string `json:"overview"`
		Plan       string `json:"plan"`
	}
	_ = json.Unmarshal(m.Params, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.closeBlockLocked()
	text := p.Plan
	if text == "" {
		text = p.Overview
	}
	rid := idKey(m.ID)
	s.reqs[rid] = &agentReq{kind: "plan", id: append(json.RawMessage(nil), m.ID...), toolCallID: p.ToolCallID}
	s.reqOrder = append(s.reqOrder, rid)
	return append(evs, ev(agent.KindPlanProposal, agent.PlanProposal{ReqID: rid, ToolUseID: p.ToolCallID, Text: text}))
}

func (s *Session) onUpdateTodos(m *message) []agent.Event {
	var p struct {
		Todos []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
		Merge bool `json:"merge"`
	}
	_ = json.Unmarshal(m.Params, &p)
	s.mu.Lock()
	if !p.Merge {
		s.todos = nil
	}
	for _, t := range p.Todos {
		e := todoEntry{ID: t.ID, Content: t.Content, Status: normStatus(t.Status)}
		found := false
		if p.Merge {
			for i := range s.todos {
				if s.todos[i].ID == e.ID {
					if e.Content != "" {
						s.todos[i].Content = e.Content
					}
					s.todos[i].Status = e.Status
					found = true
				}
			}
		}
		if !found {
			s.todos = append(s.todos, e)
		}
	}
	out := []agent.Event{ev(agent.KindTodoUpdate, agent.TodoUpdate{Todos: todoItems(s.todos)})}
	s.mu.Unlock()
	_ = s.c.reply(m.ID, map[string]any{})
	return out
}

// ---- session/update ----

func textOf(raw json.RawMessage) string {
	var c struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	if c.Type == "text" || c.Type == "" {
		return c.Text
	}
	return "[" + c.Type + "]"
}

func (s *Session) onUpdateLocked(u *update) []agent.Event {
	if u.SessionUpdate != "user_message_chunk" {
		pre := s.flushUserLocked()
		return append(pre, s.dispatchLocked(u)...)
	}
	s.userBuf.WriteString(textOf(u.Content))
	return nil
}

func (s *Session) dispatchLocked(u *update) []agent.Event {
	switch u.SessionUpdate {
	case "agent_message_chunk":
		return s.deltaLocked("text", textOf(u.Content))
	case "agent_thought_chunk":
		return s.deltaLocked("thinking", textOf(u.Content))
	case "tool_call", "tool_call_update":
		return s.onToolLocked(u)
	case "plan":
		s.todos = s.todos[:0]
		for i, e := range u.Entries {
			s.todos = append(s.todos, todoEntry{ID: fmt.Sprint(i + 1), Content: e.Content, Status: normStatus(e.Status)})
		}
		return []agent.Event{ev(agent.KindTodoUpdate, agent.TodoUpdate{Todos: todoItems(s.todos)})}
	case "current_mode_update":
		s.curMode = u.CurrentModeID
		return []agent.Event{ev(agent.KindModeChanged, agent.ModeChanged{Mode: u.CurrentModeID})}
	case "available_commands_update":
		var names []string
		for _, c := range u.AvailableCommands {
			names = append(names, c.Name)
		}
		s.commands = names
		if s.ready {
			return []agent.Event{s.sessionInitLocked()}
		}
	}
	return nil // session_info_update and anything unknown
}

func (s *Session) msgID() string { return fmt.Sprintf("t%d", s.turn) }

func (s *Session) deltaLocked(kind, text string) []agent.Event {
	if text == "" {
		return nil
	}
	var evs []agent.Event
	if s.curKind != kind {
		evs = s.closeBlockLocked()
		s.curKind = kind
	}
	s.curText.WriteString(text)
	k := agent.KindTextDelta
	if kind == "thinking" {
		k = agent.KindThinkingDelta
	}
	return append(evs, ev(k, agent.BlockDelta{MessageID: s.msgID(), Index: s.blockIdx, Text: text}))
}

// closeBlockLocked ends the open text/thinking block with message_done.
func (s *Session) closeBlockLocked() []agent.Event {
	if s.curKind == "" {
		return nil
	}
	d := agent.MessageDone{MessageID: s.msgID(), Index: s.blockIdx, BlockType: s.curKind, Text: s.curText.String()}
	if s.curKind == "text" {
		s.lastText = d.Text
	}
	s.curKind = ""
	s.curText.Reset()
	s.blockIdx++
	return []agent.Event{ev(agent.KindMessageDone, d)}
}

// flushUserLocked turns buffered session/load user_message_chunks into one
// user_message and starts a new turn id.
func (s *Session) flushUserLocked() []agent.Event {
	if s.userBuf.Len() == 0 {
		return nil
	}
	evs := s.closeBlockLocked()
	text := s.userBuf.String()
	s.userBuf.Reset()
	s.turn++
	s.blockIdx = 0
	return append(evs, agent.NewUserMessageEvent(agent.UserMessage{Text: text}))
}

func parseObj(raw json.RawMessage) map[string]any {
	var m map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func terminalStatus(s string) bool { return s == "completed" || s == "failed" }

func (s *Session) onToolLocked(u *update) []agent.Event {
	var evs []agent.Event
	ts := s.tools[u.ToolCallID]
	if ts == nil {
		evs = s.closeBlockLocked()
		raw := parseObj(u.RawInput)
		ts = &toolState{id: u.ToolCallID, title: u.Title, raw: raw, locs: u.Locations}
		ts.name = toolName(u.Kind, raw)
		s.tools[u.ToolCallID] = ts
		in := toolInput(ts.name, ts.title, s.cwd, ts.raw, ts.locs)
		ts.lastInput = in
		evs = append(evs,
			ev(agent.KindToolStart, agent.ToolStart{ID: ts.id, Name: ts.name, Input: in}),
			ev(agent.KindToolInputDone, agent.ToolInputDone{ID: ts.id, Name: ts.name, Input: in}))
	} else {
		if u.Title != "" {
			ts.title = u.Title
		}
		if r := parseObj(u.RawInput); len(r) > 0 {
			if ts.raw == nil {
				ts.raw = map[string]any{}
			}
			for k, v := range r {
				ts.raw[k] = v
			}
		}
		if len(u.Locations) > 0 {
			ts.locs = u.Locations
		}
		if in := toolInput(ts.name, ts.title, s.cwd, ts.raw, ts.locs); !bytes.Equal(in, ts.lastInput) {
			ts.lastInput = in
			evs = append(evs, ev(agent.KindToolInputDone, agent.ToolInputDone{ID: ts.id, Name: ts.name, Input: in}))
		}
	}
	for _, c := range parseToolContent(u.Content) {
		if c.Type == "diff" {
			ts.diff = DiffFromContent(c, s.cwd)
		}
	}
	if terminalStatus(u.Status) && !ts.done {
		ts.done = true
		evs = append(evs, ev(agent.KindToolResult, s.toolResultLocked(ts, u)))
	}
	return evs
}

func (s *Session) toolResultLocked(ts *toolState, u *update) agent.ToolResult {
	r := agent.ToolResult{ID: ts.id, OK: u.Status != "failed", Diff: ts.diff}
	var out string
	hasOut := false
	if raw := parseObj(u.RawOutput); raw != nil {
		hasOut = true
		_, hasStdout := raw["stdout"]
		_, hasStderr := raw["stderr"]
		switch {
		case hasStdout || hasStderr:
			so, _ := raw["stdout"].(string)
			se, _ := raw["stderr"].(string)
			out = so
			if se != "" {
				if out != "" && !strings.HasSuffix(out, "\n") {
					out += "\n"
				}
				out += se
			}
		case raw["content"] != nil:
			if c, ok := raw["content"].(string); ok {
				out = c
			} else {
				out = string(mustJSON(raw["content"]))
			}
		default:
			out = string(mustJSON(raw))
		}
		if c, ok := raw["exitCode"].(float64); ok {
			code := int(c)
			r.ExitCode = &code
			if code != 0 {
				r.OK = false
			}
		}
	}
	if out == "" {
		var texts []string
		for _, c := range parseToolContent(u.Content) {
			if c.Content.Text != "" {
				texts = append(texts, c.Content.Text)
			}
		}
		out = strings.Join(texts, "\n")
		hasOut = hasOut || out != ""
	}
	r.Output, r.Truncated = truncate(out)
	if s.rejected[ts.id] && !hasOut && r.Diff == "" {
		r.OK, r.Denied = false, true
	}
	return r
}
