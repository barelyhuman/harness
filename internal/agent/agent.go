// Package agent defines the provider-neutral agent core: the Provider and
// Session interfaces, the normalized Event stream and the model registry.
//
// # Ownership
//
// Providers are stateless descriptions. The detached run host (spec 10) owns
// the live OS process: it execs Provider.Command, pumps child stdout line by
// line into Session.Feed, appends the returned events to the run log (assigning
// Seq/RunID/TS with a Sequencer) and serves the app. A Session is the
// per-process protocol state machine; create one per spawned process.
//
// # Session contract
//
//   - Feed is called by exactly one reader goroutine, once per stdout line
//     (without the trailing newline), in order. It returns zero or more
//     normalized events (Seq, RunID and TS are zero; the host stamps them).
//     A malformed line yields an error and no events; the host should log it
//     and keep going. Feed may WRITE to the process from inside the call
//     (the Session was given the stdin writer in NewSession). Claude uses this
//     to refuse unknown CLI requests; ACP uses it to answer agent->client
//     JSON-RPC requests such as fs/* and cursor/* (and to resolve its own
//     pending outgoing requests).
//   - Start runs the provider handshake (no-op for Claude; ACP performs
//     initialize/authenticate/session new|load). The host calls it after the
//     reader goroutine is running, because the handshake responses are
//     delivered through Feed. It may block until the handshake completes or
//     ctx ends.
//   - SendUser, RespondPermission, Interrupt, SetModel and SetMode may be
//     called from any goroutine, concurrently with Feed. Implementations
//     serialize their own writes (one line/frame is written atomically).
//     SendUser and RespondPermission return events the host must append
//     immediately (user_message, permission_resolved) because the CLI does not
//     echo them in a usable form. Effects of the others (model_changed,
//     mode_changed, errors) are reported later through Feed when the provider
//     acknowledges them.
//   - Closed is called once when the child has exited. It returns closing
//     events (cancelling pending permissions, reporting abnormal exits).
//
// Permission requests are keyed by ReqID, an opaque provider string (Claude:
// the control request_id; ACP: the stringified JSON-RPC id). Options on a
// permission_request carry an ID and an ACP-style Kind
// (allow_once/allow_always/reject_once); a Decision is mapped back to the
// provider wire format by the Session, so UIs never see wire shapes.
package agent

import (
	"context"
	"encoding/json"
	"io"
)

// Provider describes one agent CLI. It holds no process state.
type Provider interface {
	ID() string
	Detect(ctx context.Context) (Install, error)
	// Models lists the models the provider offers (live when feasible, else a
	// static fallback). Model.Provider is set to ID().
	Models(ctx context.Context) ([]Model, error)
	// Command returns argv/env/cwd for the host to exec.
	Command(StartOpts) (Cmd, error)
	// NewSession creates the protocol state machine for one spawned process.
	// w is the process stdin. opts are the same options given to Command.
	NewSession(w io.Writer, opts StartOpts) Session
}

// Session is the per-process protocol state machine. See the package doc for
// the full contract.
type Session interface {
	Feed(line []byte) ([]Event, error)
	Start(ctx context.Context) error
	SendUser(UserMessage) ([]Event, error)
	RespondPermission(reqID string, d Decision) ([]Event, error)
	Interrupt() error
	SetModel(model string) error
	SetMode(mode string) error
	Closed(exitCode int, stderrTail string) []Event
}

// Install describes a detected CLI.
type Install struct {
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	// Detail carries a human readable reason when Found is false.
	Detail string `json:"detail,omitempty"`
}

// StartOpts are provider-neutral process start options.
type StartOpts struct {
	RunID string
	Cwd   string
	// Binary overrides the CLI path (defaults to the detected install).
	Binary string
	Model  string
	// Mode is the provider permission mode (Claude: default, acceptEdits,
	// plan, bypassPermissions...; ACP: agent, plan, ask).
	Mode   string
	Effort string
	// ResumeID is the provider session id to resume.
	ResumeID        string
	MCPConfig       []string
	AddDirs         []string
	AllowedTools    []string
	DisallowedTools []string
	// Settings are JSON objects merged (later wins, objects deep-merged)
	// into the single --settings argument.
	Settings []json.RawMessage
	// AppendSystemPrompt chunks are combined into one argument.
	AppendSystemPrompt []string
	// Env entries (KEY=VALUE) added to the host environment.
	Env []string
}

// Cmd is what the host should exec.
type Cmd struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	Env  []string `json:"env,omitempty"`
	Dir  string   `json:"dir,omitempty"`
}

// UserMessage is a user turn: text plus optional image attachments.
type UserMessage struct {
	Text   string  `json:"text"`
	Images []Image `json:"images,omitempty"`
}

// Image is an image attachment (raw bytes, base64-encoded on the wire).
type Image struct {
	MediaType string `json:"mediaType"`
	Name      string `json:"name,omitempty"`
	Data      []byte `json:"data"`
}

// Behavior is a permission decision.
type Behavior string

const (
	BehaviorAllow       Behavior = "allow"
	BehaviorAllowAlways Behavior = "allow_always"
	BehaviorDeny        Behavior = "deny"
)

// Decision answers a permission_request or plan_proposal.
type Decision struct {
	Behavior Behavior `json:"behavior"`
	// Message is feedback for the model on deny (or plan rejection).
	Message string `json:"message,omitempty"`
	// UpdatedInput replaces the tool input on allow. Empty means "unchanged"
	// (the Session echoes the original input).
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	// OptionID optionally selects a provider option explicitly.
	OptionID string `json:"optionId,omitempty"`
}
