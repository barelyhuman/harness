package transcript

import (
	"fmt"
	"strings"
	"sync"

	"github.com/sid/harness/internal/agent"
)

// Meta is run-level state derived from the same events (for chips, spinners).
type Meta struct {
	Cwd               string  `json:"cwd,omitempty"`
	Model             string  `json:"model,omitempty"`
	Mode              string  `json:"mode,omitempty"`
	ProviderSessionID string  `json:"providerSessionId,omitempty"`
	Running           bool    `json:"running"`
	ContextWindow     int     `json:"contextWindow,omitempty"`
	TotalCostUSD      float64 `json:"totalCostUsd,omitempty"`
	LastSeq           uint64  `json:"lastSeq"`
}

type node struct {
	b      Block
	kids   []*node
	parent *node
}

// Builder folds events into blocks. It is safe for concurrent use, and
// deterministic: the same events always yield the same blocks and ids.
type Builder struct {
	mu     sync.Mutex
	roots  []*node
	idx    map[string]*node
	cwd    string
	meta   Meta
	nUser  int
	nRes   int
	nErr   int
	usage  *agent.Usage
	hidden map[string]string // tool id -> "todo" | "plan"
	// permByTool: tool use id -> pending permission/plan block id.
	permByTool map[string]string
	planByTool map[string]string
}

// NewBuilder returns an empty builder.
func NewBuilder() *Builder {
	return &Builder{
		idx:        map[string]*node{},
		hidden:     map[string]string{},
		permByTool: map[string]string{},
		planByTool: map[string]string{},
	}
}

// Meta returns run-level state.
func (b *Builder) Meta() Meta {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.meta
}

// Snapshot returns a deep copy of the whole transcript.
func (b *Builder) Snapshot() []Block {
	b.mu.Lock()
	defer b.mu.Unlock()
	return snapNodes(b.roots)
}

// SnapshotPatch wraps Snapshot as a snapshot patch (sent when a channel opens).
func (b *Builder) SnapshotPatch() Patch {
	return Patch{Op: OpSnapshot, Blocks: b.Snapshot()}
}

func snapNodes(ns []*node) []Block {
	out := make([]Block, 0, len(ns))
	for _, n := range ns {
		blk := cloneBlock(n.b)
		blk.Children = snapNodes(n.kids)
		if len(blk.Children) == 0 {
			blk.Children = nil
		}
		out = append(out, blk)
	}
	return out
}

// cloneBlock copies the block and its pointed-to parts (Children excluded).
func cloneBlock(b Block) Block {
	b.Children = nil
	if b.Tool != nil {
		t := *b.Tool
		if t.ExitCode != nil {
			c := *t.ExitCode
			t.ExitCode = &c
		}
		b.Tool = &t
	}
	if b.Permission != nil {
		p := *b.Permission
		b.Permission = &p
	}
	if b.Plan != nil {
		p := *b.Plan
		b.Plan = &p
	}
	if b.Footer != nil {
		f := *b.Footer
		b.Footer = &f
	}
	if b.Error != nil {
		e := *b.Error
		b.Error = &e
	}
	b.Todos = append([]agent.TodoItem(nil), b.Todos...)
	if len(b.Todos) == 0 {
		b.Todos = nil
	}
	return b
}

func (n *node) parentID() string {
	if n.parent != nil {
		return n.parent.b.ID
	}
	return ""
}

func (b *Builder) appendPatch(n *node, afterID string) Patch {
	blk := cloneBlock(n.b)
	return Patch{Op: OpAppend, ParentID: n.parentID(), AfterID: afterID, Block: &blk}
}

func updatePatch(n *node) Patch {
	blk := cloneBlock(n.b)
	return Patch{Op: OpUpdate, BlockID: n.b.ID, ParentID: n.parentID(), Block: &blk}
}

// add attaches n under the tool block parentToolID (if known) or at the top.
func (b *Builder) add(n *node, parentToolID string) Patch {
	if parentToolID != "" {
		if p := b.idx["tool:"+parentToolID]; p != nil {
			n.parent = p
			p.kids = append(p.kids, n)
			b.idx[n.b.ID] = n
			return b.appendPatch(n, "")
		}
	}
	b.roots = append(b.roots, n)
	b.idx[n.b.ID] = n
	return b.appendPatch(n, "")
}

// addAfter inserts n directly after sibling `after` (same container).
func (b *Builder) addAfter(n *node, after *node) Patch {
	list := &b.roots
	if after.parent != nil {
		n.parent = after.parent
		list = &after.parent.kids
	}
	for i, s := range *list {
		if s == after {
			// Insert immediately after the tool card.
			*list = append(*list, nil)
			copy((*list)[i+2:], (*list)[i+1:])
			(*list)[i+1] = n
			b.idx[n.b.ID] = n
			return b.appendPatch(n, after.b.ID)
		}
	}
	return b.add(n, "")
}

// Apply folds one event into the transcript and returns the resulting patches.
func (b *Builder) Apply(ev agent.Event) []Patch {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ev.Seq > b.meta.LastSeq {
		b.meta.LastSeq = ev.Seq
	}
	switch ev.Kind {
	case agent.KindUserMessage:
		return b.onUser(ev)
	case agent.KindSessionInit:
		d := agent.MustDecode[agent.SessionInit](ev)
		b.meta.ProviderSessionID = d.ProviderSessionID
		if d.Cwd != "" {
			b.cwd, b.meta.Cwd = d.Cwd, d.Cwd
		}
		if d.Model != "" {
			b.meta.Model = d.Model
		}
		if d.Mode != "" {
			b.meta.Mode = d.Mode
		}
	case agent.KindRunStarted:
		d := agent.MustDecode[agent.RunStarted](ev)
		if d.Cwd != "" && b.cwd == "" {
			b.cwd, b.meta.Cwd = d.Cwd, d.Cwd
		}
	case agent.KindStatus:
		if agent.MustDecode[agent.Status](ev).State == "requesting" {
			b.meta.Running = true
		}
	case agent.KindModeChanged:
		b.meta.Mode = agent.MustDecode[agent.ModeChanged](ev).Mode
	case agent.KindModelChanged:
		b.meta.Model = agent.MustDecode[agent.ModelChanged](ev).Model
	case agent.KindTextDelta:
		return b.onDelta(ev, "text", BlockAssistantText)
	case agent.KindThinkingDelta:
		return b.onDelta(ev, "think", BlockThinking)
	case agent.KindMessageDone:
		return b.onMessageDone(ev)
	case agent.KindToolStart:
		d := agent.MustDecode[agent.ToolStart](ev)
		return b.onToolStart(ev, d.ID, d.Name, d.Input)
	case agent.KindToolInputDone:
		return b.onToolInputDone(ev)
	case agent.KindToolResult:
		return b.onToolResult(ev)
	case agent.KindPermissionRequest:
		return b.onPermissionRequest(ev)
	case agent.KindPermissionResolved:
		return b.onPermissionResolved(ev)
	case agent.KindPermissionCancelled:
		return b.onPermissionCancelled(ev)
	case agent.KindPlanProposal:
		return b.onPlan(ev)
	case agent.KindTodoUpdate:
		return b.onTodo(ev)
	case agent.KindUsage:
		u := agent.MustDecode[agent.Usage](ev)
		b.usage = &u
		b.meta.TotalCostUSD = u.TotalCostUSD
		if u.ContextWindow > 0 {
			b.meta.ContextWindow = u.ContextWindow
		}
	case agent.KindRunResult:
		return b.onRunResult(ev)
	case agent.KindError:
		return b.onError(ev)
	}
	return nil
}

func (b *Builder) onUser(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.UserMessageData](ev)
	b.nUser++
	b.meta.Running = true
	n := &node{b: Block{ID: fmt.Sprintf("user:%d", b.nUser), Type: BlockUser, TS: ev.TS, Text: d.Text, Images: d.Images}}
	return []Patch{b.add(n, "")}
}

func (b *Builder) onDelta(ev agent.Event, prefix string, typ BlockType) []Patch {
	d := agent.MustDecode[agent.BlockDelta](ev)
	if d.Text == "" {
		return nil
	}
	id := fmt.Sprintf("%s:%s:%d", prefix, d.MessageID, d.Index)
	if n := b.idx[id]; n != nil {
		n.b.Text += d.Text
		return []Patch{{Op: OpUpdate, BlockID: id, ParentID: n.parentID(), TextDelta: d.Text}}
	}
	n := &node{b: Block{ID: id, Type: typ, TS: ev.TS, Text: d.Text, Streaming: true}}
	if typ == BlockThinking {
		n.b.Title, n.b.Collapsed = "Thinking", true
	}
	return []Patch{b.add(n, ev.ParentToolID)}
}

func (b *Builder) onMessageDone(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.MessageDone](ev)
	prefix, typ := "text", BlockAssistantText
	if d.BlockType == "thinking" {
		prefix, typ = "think", BlockThinking
	}
	id := fmt.Sprintf("%s:%s:%d", prefix, d.MessageID, d.Index)
	n := b.idx[id]
	if n == nil {
		if strings.TrimSpace(d.Text) == "" {
			return nil // empty (encrypted) thinking or empty text: nothing to show
		}
		n = &node{b: Block{ID: id, Type: typ, TS: ev.TS, Text: d.Text}}
		if typ == BlockThinking {
			n.b.Title, n.b.Collapsed = "Thinking", true
		}
		return []Patch{b.add(n, ev.ParentToolID)}
	}
	if d.Text != "" {
		n.b.Text = d.Text
	}
	n.b.Streaming = false
	return []Patch{updatePatch(n)}
}

func (b *Builder) toolNode(id, name string, ev agent.Event) (*node, *Patch) {
	if n := b.idx["tool:"+id]; n != nil {
		return n, nil
	}
	f := describeTool(name, nil, b.cwd)
	n := &node{b: Block{
		ID: "tool:" + id, Type: BlockTool, TS: ev.TS, Title: f.Title,
		Collapsed: f.Kind != KindBash && f.Kind != KindEdit && f.Kind != KindWrite && f.Kind != KindTask,
		Tool:      &ToolBlock{ToolID: id, Name: name, Kind: f.Kind, Status: ToolPending},
	}}
	p := b.add(n, ev.ParentToolID)
	return n, &p
}

func (b *Builder) hide(id, name string) bool {
	switch name {
	case "TodoWrite":
		b.hidden[id] = "todo"
	case "ExitPlanMode":
		b.hidden[id] = "plan"
	default:
		return false
	}
	return true
}

func (b *Builder) onToolStart(ev agent.Event, id, name string, _ []byte) []Patch {
	if b.hide(id, name) {
		return nil
	}
	_, p := b.toolNode(id, name, ev)
	if p == nil {
		return nil
	}
	return []Patch{*p}
}

func (b *Builder) onToolInputDone(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.ToolInputDone](ev)
	if b.hide(d.ID, d.Name) {
		return nil
	}
	var out []Patch
	n, created := b.toolNode(d.ID, d.Name, ev)
	if created != nil {
		out = append(out, *created)
	}
	t := n.b.Tool
	t.Input = d.Input
	f := describeTool(t.Name, d.Input, b.cwd)
	t.Kind, t.Path, t.Command = f.Kind, f.Path, f.Command
	n.b.Title = f.Title
	if t.Diff == "" && (f.Kind == KindEdit || f.Kind == KindWrite) {
		if diff := agent.DiffFromToolInput(t.Name, d.Input); diff != "" {
			t.Diff, t.DiffPreview = diff, true
			t.Added, t.Removed = agent.DiffStat(diff)
		}
	}
	if t.Status == ToolPending && b.permByTool[d.ID] == "" {
		t.Status = ToolRunning
	}
	return append(out, updatePatch(n))
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// bashCollapseLines is the output length above which a Bash card starts
// collapsed (spec 11: collapsed after 20 lines).
const bashCollapseLines = 20

func (b *Builder) onToolResult(ev agent.Event) []Patch {
	r := agent.MustDecode[agent.ToolResult](ev)
	switch b.hidden[r.ID] {
	case "todo":
		return nil
	case "plan":
		reqID := b.planByTool[r.ID]
		if n := b.idx["plan:"+reqID]; n != nil && n.b.Plan.State == StatePending {
			if r.OK {
				n.b.Plan.State = StateAllowed
			} else {
				n.b.Plan.State = StateDenied
			}
			n.b.Collapsed = true
			delete(b.planByTool, r.ID)
			return []Patch{updatePatch(n)}
		}
		return nil
	}
	var out []Patch
	n, created := b.toolNode(r.ID, "", ev)
	if created != nil {
		out = append(out, *created)
	}
	t := n.b.Tool
	if r.OK {
		t.Status = ToolOK
	} else {
		t.Status = ToolError
	}
	t.Output, t.OutputTruncated, t.OutputLines = r.Output, r.Truncated, lineCount(r.Output)
	t.ExitCode, t.Denied, t.Interrupted = r.ExitCode, r.Denied, r.Interrupted
	if r.Diff != "" {
		t.Diff, t.DiffPreview = r.Diff, false
		t.Added, t.Removed = agent.DiffStat(r.Diff)
	} else if !r.OK && t.DiffPreview {
		t.Diff, t.DiffPreview, t.Added, t.Removed = "", false, 0, 0 // the change did not happen
	}
	switch {
	case r.Denied:
		n.b.Collapsed = true
	case !r.OK:
		n.b.Collapsed = false
	case t.Kind == KindBash:
		n.b.Collapsed = t.OutputLines > bashCollapseLines
	case t.Kind == KindEdit || t.Kind == KindWrite:
		n.b.Collapsed = false
	default:
		n.b.Collapsed = true
	}
	// Resolve a still-pending permission card from the outcome.
	if reqID := b.permByTool[r.ID]; reqID != "" {
		if pn := b.idx["perm:"+reqID]; pn != nil && pn.b.Permission.State == StatePending {
			pn.b.Permission.State = StateAllowed
			if r.Denied {
				pn.b.Permission.State = StateDenied
			}
			pn.b.Collapsed = true
			out = append(out, updatePatch(pn))
		}
		delete(b.permByTool, r.ID)
	}
	return append(out, updatePatch(n))
}

func (b *Builder) onPermissionRequest(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.PermissionRequest](ev)
	id := "perm:" + d.ReqID
	if b.idx[id] != nil {
		return nil
	}
	var out []Patch
	title := "Allow " + d.Tool + "?"
	if tn := b.idx["tool:"+d.ToolUseID]; tn != nil {
		title = "Allow " + tn.b.Title + "?"
		if tn.b.Tool.Status == ToolRunning {
			tn.b.Tool.Status = ToolPending
			out = append(out, updatePatch(tn))
		}
	} else if f := describeTool(d.Tool, d.Input, b.cwd); f.Title != "" {
		title = "Allow " + f.Title + "?"
	}
	n := &node{b: Block{
		ID: id, Type: BlockPermission, TS: ev.TS, Title: title,
		Permission: &PermissionBlock{
			ReqID: d.ReqID, ToolUseID: d.ToolUseID, Tool: d.Tool, Description: d.Description,
			Reason: d.DecisionReason, Input: d.Input, Options: d.Options, State: StatePending,
		},
	}}
	if d.ToolUseID != "" {
		b.permByTool[d.ToolUseID] = d.ReqID
	}
	if tn := b.idx["tool:"+d.ToolUseID]; tn != nil {
		return append(out, b.addAfter(n, tn))
	}
	return append(out, b.add(n, ev.ParentToolID))
}

func (b *Builder) onPermissionResolved(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.PermissionResolved](ev)
	var out []Patch
	allowed := d.Behavior != agent.BehaviorDeny
	state := StateAllowed
	if !allowed {
		state = StateDenied
	}
	if n := b.idx["perm:"+d.ReqID]; n != nil {
		p := n.b.Permission
		p.State, p.Message, p.Always = state, d.Message, d.Behavior == agent.BehaviorAllowAlways
		n.b.Collapsed = true
		out = append(out, updatePatch(n))
	} else if n := b.idx["plan:"+d.ReqID]; n != nil {
		n.b.Plan.State, n.b.Plan.Message = state, d.Message
		n.b.Collapsed = true
		out = append(out, updatePatch(n))
	}
	if d.ToolUseID != "" {
		delete(b.permByTool, d.ToolUseID)
		if tn := b.idx["tool:"+d.ToolUseID]; tn != nil && allowed && tn.b.Tool.Status == ToolPending {
			tn.b.Tool.Status = ToolRunning
			out = append(out, updatePatch(tn))
		}
	}
	return out
}

func (b *Builder) onPermissionCancelled(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.PermissionCancelled](ev)
	delete(b.permByTool, d.ToolUseID)
	if n := b.idx["perm:"+d.ReqID]; n != nil && n.b.Permission.State == StatePending {
		n.b.Permission.State = StateCancelled
		n.b.Collapsed = true
		return []Patch{updatePatch(n)}
	}
	if n := b.idx["plan:"+d.ReqID]; n != nil && n.b.Plan.State == StatePending {
		n.b.Plan.State = StateCancelled
		n.b.Collapsed = true
		return []Patch{updatePatch(n)}
	}
	return nil
}

func (b *Builder) onPlan(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.PlanProposal](ev)
	id := "plan:" + d.ReqID
	if b.idx[id] != nil {
		return nil
	}
	n := &node{b: Block{
		ID: id, Type: BlockPlan, TS: ev.TS, Title: "Plan", Text: d.Text,
		Plan: &PlanBlock{ReqID: d.ReqID, ToolUseID: d.ToolUseID, FilePath: d.FilePath, State: StatePending},
	}}
	if d.ToolUseID != "" {
		b.planByTool[d.ToolUseID] = d.ReqID
	}
	return []Patch{b.add(n, ev.ParentToolID)}
}

func (b *Builder) onTodo(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.TodoUpdate](ev)
	done := 0
	for _, t := range d.Todos {
		if t.Status == "completed" {
			done++
		}
	}
	title := fmt.Sprintf("Todos %d/%d", done, len(d.Todos))
	if n := b.idx["todo"]; n != nil {
		n.b.Todos, n.b.Title = d.Todos, title
		return []Patch{updatePatch(n)}
	}
	n := &node{b: Block{ID: "todo", Type: BlockTodo, TS: ev.TS, Title: title, Todos: d.Todos}}
	return []Patch{b.add(n, "")}
}

func fmtTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func (b *Builder) onRunResult(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.RunResult](ev)
	var out []Patch
	b.meta.Running = false
	if d.ProviderSessionID != "" {
		b.meta.ProviderSessionID = d.ProviderSessionID
	}
	// Anything still streaming is finished now.
	for _, n := range b.allNodes() {
		changed := false
		if n.b.Streaming {
			n.b.Streaming, changed = false, true
		}
		if t := n.b.Tool; t != nil && (t.Status == ToolPending || t.Status == ToolRunning) {
			t.Status, t.Interrupted, changed = ToolError, d.Status == agent.ResultInterrupted, true
		}
		if changed {
			out = append(out, updatePatch(n))
		}
	}
	f := &FooterBlock{Status: d.Status, DurationMS: d.DurationMS}
	var parts []string
	switch d.Status {
	case agent.ResultInterrupted:
		parts = append(parts, "Interrupted")
	case agent.ResultError:
		parts = append(parts, "Failed")
	}
	if u := b.usage; u != nil {
		f.InputTokens = u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
		f.OutputTokens, f.CacheTokens, f.CostUSD = u.OutputTokens, u.CacheReadTokens, u.CostUSD
		f.Model, f.ContextWindow = u.Model, u.ContextWindow
		if f.InputTokens+f.OutputTokens > 0 {
			parts = append(parts, fmtTokens(f.InputTokens)+" in", fmtTokens(f.OutputTokens)+" out")
		}
		if f.CostUSD > 0 {
			parts = append(parts, fmt.Sprintf("$%.4f", f.CostUSD))
		}
	}
	if d.DurationMS > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", float64(d.DurationMS)/1000))
	}
	if len(parts) == 0 {
		parts = append(parts, "Done")
	}
	f.Summary = strings.Join(parts, " · ")
	b.usage = nil
	b.nRes++
	n := &node{b: Block{ID: fmt.Sprintf("result:%d", b.nRes), Type: BlockResultFooter, TS: ev.TS, Footer: f}}
	return append(out, b.add(n, ""))
}

func (b *Builder) allNodes() []*node {
	var out []*node
	var walk func(ns []*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			out = append(out, n)
			walk(n.kids)
		}
	}
	walk(b.roots)
	return out
}

func (b *Builder) onError(ev agent.Event) []Patch {
	d := agent.MustDecode[agent.ErrorData](ev)
	title := "Error"
	switch d.Code {
	case agent.ErrResumeNotFound:
		title = "Session not found"
	case agent.ErrProcessExit:
		title = "Agent exited"
	}
	b.nErr++
	n := &node{b: Block{
		ID: fmt.Sprintf("error:%d", b.nErr), Type: BlockError, TS: ev.TS, Title: title, Text: d.Message,
		Error: &ErrorBlock{Code: d.Code, Fatal: d.Fatal},
	}}
	return []Patch{b.add(n, "")}
}
