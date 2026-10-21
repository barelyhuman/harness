// Package transcript turns the normalized agent event stream into the view
// model the chat UI renders. Go owns this model: the frontend only applies
// Patches and draws Blocks, so every Block carries display-ready fields.
package transcript

import (
	"encoding/json"
	"time"

	"github.com/sid/harness/internal/agent"
)

// BlockType is the kind of a transcript block.
type BlockType string

const (
	BlockUser          BlockType = "user"
	BlockAssistantText BlockType = "assistant_text"
	BlockThinking      BlockType = "thinking"
	BlockTool          BlockType = "tool"
	BlockPermission    BlockType = "permission"
	BlockPlan          BlockType = "plan"
	BlockTodo          BlockType = "todo"
	BlockResultFooter  BlockType = "result_footer"
	BlockError         BlockType = "error"
)

// Tool statuses.
const (
	ToolPending = "pending" // input streaming or awaiting permission
	ToolRunning = "running"
	ToolOK      = "ok"
	ToolError   = "error"
)

// Permission / plan states.
const (
	StatePending   = "pending"
	StateAllowed   = "allowed"
	StateDenied    = "denied"
	StateCancelled = "cancelled"
)

// Tool kinds drive which card the UI draws.
const (
	KindBash   = "bash"
	KindEdit   = "edit"
	KindWrite  = "write"
	KindRead   = "read"
	KindSearch = "search"
	KindTask   = "task"
	KindWeb    = "web"
	KindMCP    = "mcp"
	KindOther  = "other"
)

// Block is one transcript entry. Only the sub-struct matching Type is set.
type Block struct {
	ID        string    `json:"id"`
	Type      BlockType `json:"type"`
	TS        time.Time `json:"ts,omitempty"`
	Title     string    `json:"title,omitempty"`
	Text      string    `json:"text,omitempty"`
	Streaming bool      `json:"streaming,omitempty"`
	Collapsed bool      `json:"collapsed,omitempty"`

	Tool       *ToolBlock       `json:"tool,omitempty"`
	Permission *PermissionBlock `json:"permission,omitempty"`
	Plan       *PlanBlock       `json:"plan,omitempty"`
	Todos      []agent.TodoItem `json:"todos,omitempty"`
	Footer     *FooterBlock     `json:"footer,omitempty"`
	Error      *ErrorBlock      `json:"error,omitempty"`
	Images     []agent.ImageRef `json:"images,omitempty"`

	// Children are nested blocks (tool calls made by a Task subagent). In
	// update patches Children is omitted; child changes arrive as their own
	// patches with ParentID set.
	Children []Block `json:"children,omitempty"`
}

// ToolBlock is a tool call card.
type ToolBlock struct {
	ToolID          string          `json:"toolId"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	Input           json.RawMessage `json:"input,omitempty"`
	Path            string          `json:"path,omitempty"`    // display path (relative to cwd when possible)
	Command         string          `json:"command,omitempty"` // Bash command, full
	Output          string          `json:"output,omitempty"`
	OutputLines     int             `json:"outputLines,omitempty"`
	OutputTruncated bool            `json:"outputTruncated,omitempty"`
	Diff            string          `json:"diff,omitempty"` // unified diff
	DiffPreview     bool            `json:"diffPreview,omitempty"`
	Added           int             `json:"added,omitempty"`
	Removed         int             `json:"removed,omitempty"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	Denied          bool            `json:"denied,omitempty"`
	Interrupted     bool            `json:"interrupted,omitempty"`
}

// PermissionBlock is an approval card.
type PermissionBlock struct {
	ReqID       string                   `json:"reqId"`
	ToolUseID   string                   `json:"toolUseId,omitempty"`
	Tool        string                   `json:"tool"`
	Description string                   `json:"description,omitempty"`
	Reason      string                   `json:"reason,omitempty"`
	Input       json.RawMessage          `json:"input,omitempty"`
	Options     []agent.PermissionOption `json:"options,omitempty"`
	State       string                   `json:"state"`
	Always      bool                     `json:"always,omitempty"`
	Message     string                   `json:"message,omitempty"`
}

// PlanBlock is a plan awaiting approval.
type PlanBlock struct {
	ReqID     string `json:"reqId"`
	ToolUseID string `json:"toolUseId,omitempty"`
	FilePath  string `json:"filePath,omitempty"`
	State     string `json:"state"` // pending | allowed | denied | cancelled
	Message   string `json:"message,omitempty"`
}

// FooterBlock summarizes a finished turn.
type FooterBlock struct {
	Status        string  `json:"status"` // success | interrupted | error
	Summary       string  `json:"summary"`
	InputTokens   int     `json:"inputTokens"`
	OutputTokens  int     `json:"outputTokens"`
	CacheTokens   int     `json:"cacheTokens"`
	CostUSD       float64 `json:"costUsd"`
	DurationMS    int64   `json:"durationMs,omitempty"`
	Model         string  `json:"model,omitempty"`
	ContextWindow int     `json:"contextWindow,omitempty"`
}

// ErrorBlock is a non-tool error.
type ErrorBlock struct {
	Code  string `json:"code"`
	Fatal bool   `json:"fatal,omitempty"`
}

// Op is a patch operation.
type Op string

const (
	OpAppend   Op = "append"
	OpUpdate   Op = "update"
	OpSnapshot Op = "snapshot"
)

// Patch is one change to the transcript.
//
//   - append: Block is added to the end of the top level, or to the end of the
//     Children of ParentID when set. If AfterID is set it is inserted right
//     after that sibling instead (permission cards follow their tool).
//   - update: if TextDelta is set, append it to the Text of BlockID (Block is
//     nil); otherwise Block replaces the block BlockID (keeping its children).
//     ParentID locates nested blocks.
//   - snapshot: Blocks is the full transcript (sent when a channel opens).
type Patch struct {
	Op        Op      `json:"op"`
	BlockID   string  `json:"blockId,omitempty"`
	ParentID  string  `json:"parentId,omitempty"`
	AfterID   string  `json:"afterId,omitempty"`
	Block     *Block  `json:"block,omitempty"`
	TextDelta string  `json:"textDelta,omitempty"`
	Blocks    []Block `json:"blocks,omitempty"`
}

// Reduce applies a patch to blocks and returns the result. It defines the
// patch semantics (the frontend implements the same reducer).
func Reduce(blocks []Block, p Patch) []Block {
	switch p.Op {
	case OpSnapshot:
		return append([]Block(nil), p.Blocks...)
	case OpAppend:
		if p.Block == nil {
			return blocks
		}
		if p.ParentID == "" {
			return insertAfter(blocks, p.AfterID, *p.Block)
		}
		if parent := find(blocks, p.ParentID); parent != nil {
			parent.Children = insertAfter(parent.Children, p.AfterID, *p.Block)
		}
		return blocks
	case OpUpdate:
		b := find(blocks, p.BlockID)
		if b == nil {
			return blocks
		}
		if p.TextDelta != "" {
			b.Text += p.TextDelta
			return blocks
		}
		if p.Block != nil {
			kids := b.Children
			*b = *p.Block
			b.Children = kids
		}
	}
	return blocks
}

func find(blocks []Block, id string) *Block {
	for i := range blocks {
		if blocks[i].ID == id {
			return &blocks[i]
		}
		if c := find(blocks[i].Children, id); c != nil {
			return c
		}
	}
	return nil
}

func insertAfter(list []Block, afterID string, b Block) []Block {
	if afterID != "" {
		for i := range list {
			if list[i].ID == afterID {
				list = append(list, Block{})
				copy(list[i+2:], list[i+1:])
				list[i+1] = b
				return list
			}
		}
	}
	return append(list, b)
}
