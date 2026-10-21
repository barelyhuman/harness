// Package acp is a generic Agent Client Protocol client: JSON-RPC 2.0 over
// NDJSON stdio, driven through the agent.Session contract. Provider packages
// (internal/agent/cursor) configure it. The authoritative protocol notes for
// Cursor live in docs/spikes/cursor-acp.md.
//
// # Concurrency
//
// The host calls Feed from exactly one reader goroutine and calls Start,
// SendUser, RespondPermission, Interrupt, SetModel, SetMode from others.
// Start is a blocking request/response sequence whose responses are delivered
// by Feed. This cannot deadlock because:
//
//   - every outgoing request registers a pending entry (buffered channel of
//     capacity 1) BEFORE its line is written, so a response can never beat
//     its registration;
//   - Feed never waits on anything Start owns: it removes the pending entry,
//     runs the request's result handler (which produces events), and does a
//     non-blocking send on the buffered channel;
//   - Start waits only on that channel (or ctx / Closed) and holds no lock
//     while waiting;
//   - the session mutex is never held across a write to the agent or across a
//     wait.
package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/sid/harness/internal/agent"
)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	s := fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
	if d := dataDetail(e.Data); d != "" {
		s += " (" + d + ")"
	}
	return s
}

// dataDetail extracts data.message / data.details from an error's data.
func dataDetail(raw json.RawMessage) string {
	var d struct {
		Message string `json:"message"`
		Details string `json:"details"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return ""
	}
	if d.Message != "" {
		return d.Message
	}
	return d.Details
}

// Standard JSON-RPC error codes.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeUnsupported    = -32000
)

// message is any inbound or outbound JSON-RPC frame.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (m *message) hasID() bool {
	return len(m.ID) > 0 && !bytes.Equal(m.ID, []byte("null"))
}

func (m *message) isRequest() bool  { return m.Method != "" && m.hasID() }
func (m *message) isNotify() bool   { return m.Method != "" && !m.hasID() }
func (m *message) isResponse() bool { return m.Method == "" && m.hasID() }

// idKey is the canonical string of an id (numbers and strings alike).
func idKey(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(bytes.TrimSpace(raw))
}

type outMsg struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Method  string    `json:"method,omitempty"`
	Params  any       `json:"params,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

// callResult is what a waiting caller receives.
type callResult struct {
	Result json.RawMessage
	Err    error // *RPCError, or a transport error
}

// call is one outstanding outgoing request.
type call struct {
	ch chan callResult // capacity 1; filled exactly once
	// onResult runs on the Feed goroutine and may produce events.
	onResult func(result json.RawMessage, rerr *RPCError) []agent.Event
}

// conn is the JSON-RPC framer: id allocation, pending map, atomic writes.
type conn struct {
	wmu sync.Mutex
	w   io.Writer

	mu      sync.Mutex
	nextID  int64
	pending map[int64]*call
	closed  bool
}

func newConn(w io.Writer) *conn {
	return &conn{w: w, nextID: 1, pending: map[int64]*call{}}
}

var errConnClosed = errors.New("acp: connection closed")

// writeFrame writes one NDJSON line atomically.
func (c *conn) writeFrame(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// request registers a pending call and then writes the request.
func (c *conn) request(method string, params any, onResult func(json.RawMessage, *RPCError) []agent.Event) (*call, error) {
	cl := &call{ch: make(chan callResult, 1), onResult: onResult}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errConnClosed
	}
	id := c.nextID
	c.nextID++
	c.pending[id] = cl
	c.mu.Unlock()
	if err := c.writeFrame(outMsg{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	return cl, nil
}

func (c *conn) notify(method string, params any) error {
	return c.writeFrame(outMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *conn) reply(id json.RawMessage, result any) error {
	if result == nil {
		result = struct{}{}
	}
	return c.writeFrame(outMsg{JSONRPC: "2.0", ID: id, Result: result})
}

func (c *conn) replyErr(id json.RawMessage, code int, msg string) error {
	return c.writeFrame(outMsg{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}})
}

// complete resolves the pending call for a response. It returns the events
// the call's handler produced, and false when no such call exists.
func (c *conn) complete(m *message) ([]agent.Event, bool) {
	id, err := strconv.ParseInt(idKey(m.ID), 10, 64)
	if err != nil {
		return nil, false
	}
	c.mu.Lock()
	cl, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	var evs []agent.Event
	if cl.onResult != nil {
		evs = cl.onResult(m.Result, m.Error)
	}
	res := callResult{Result: m.Result}
	if m.Error != nil {
		res.Err = m.Error
	}
	cl.ch <- res // capacity 1, filled once: never blocks
	return evs, true
}

// failAll fails every pending call (process exit) and rejects new ones.
func (c *conn) failAll(err error) {
	c.mu.Lock()
	c.closed = true
	p := c.pending
	c.pending = map[int64]*call{}
	c.mu.Unlock()
	for _, cl := range p {
		cl.ch <- callResult{Err: err}
	}
}
