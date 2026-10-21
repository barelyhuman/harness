package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rpcMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type client struct {
	id     int
	conn   Conn
	out    *queue
	docs   map[string]bool
	gen    int
	closed bool
}

type server struct {
	m    *Manager
	key  string
	root string

	startMu sync.Mutex // serializes start/stop

	mu         sync.Mutex
	cfg        ServerConfig
	state      string
	refs       int
	pid        int
	gen        int
	stopping   bool
	cmd        *exec.Cmd
	stdin      io.Closer
	wq         *queue
	done       chan struct{}
	pending    map[string]chan rpcMsg
	nextGo     int
	initResult json.RawMessage
	clients    map[int]*client
	nextClient int
	docs       map[string]int
	diags      map[string]json.RawMessage
	idleTimer  *time.Timer
	idleTok    int
}

func newServer(m *Manager, key, root string, cfg ServerConfig) *server {
	return &server{m: m, key: key, root: root, cfg: cfg, state: stStopped}
}

func fileURI(p string) string { return (&url.URL{Scheme: "file", Path: p}).String() }

func clientCaps() map[string]any {
	return map[string]any{
		"general": map[string]any{"positionEncodings": []string{"utf-16"}},
		"workspace": map[string]any{
			"workspaceFolders": true, "configuration": true,
			"applyEdit":     true,
			"workspaceEdit": map[string]any{"documentChanges": true},
		},
		"window": map[string]any{"workDoneProgress": true},
		"textDocument": map[string]any{
			"synchronization": map[string]any{"dynamicRegistration": false, "didSave": true, "willSave": false},
			"completion": map[string]any{
				"completionItem": map[string]any{
					"snippetSupport": true, "documentationFormat": []string{"markdown", "plaintext"},
					"resolveSupport":    map[string]any{"properties": []string{"documentation", "detail", "additionalTextEdits"}},
					"deprecatedSupport": true, "insertReplaceSupport": true,
				},
				"contextSupport": true,
			},
			"hover":           map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
			"signatureHelp":   map[string]any{"signatureInformation": map[string]any{"documentationFormat": []string{"markdown", "plaintext"}, "parameterInformation": map[string]any{"labelOffsetSupport": true}}, "contextSupport": true},
			"definition":      map[string]any{"linkSupport": true},
			"references":      map[string]any{},
			"documentSymbol":  map[string]any{"hierarchicalDocumentSymbolSupport": true},
			"rename":          map[string]any{"prepareSupport": true},
			"formatting":      map[string]any{},
			"rangeFormatting": map[string]any{},
			"codeAction": map[string]any{
				"codeActionLiteralSupport": map[string]any{"codeActionKind": map[string]any{"valueSet": []string{"", "quickfix", "refactor", "refactor.extract", "refactor.inline", "refactor.rewrite", "source", "source.organizeImports"}}},
				"isPreferredSupport":       true, "resolveSupport": map[string]any{"properties": []string{"edit"}},
			},
			"publishDiagnostics": map[string]any{"relatedInformation": true, "versionSupport": true},
		},
	}
}

// start spawns and initializes the process. startMu held.
func (s *server) start(bin string) error {
	cmd := exec.Command(bin, s.cfg.Args...)
	cmd.Dir = s.root
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	wq := newQueue(func(b []byte) { WriteMessage(stdin, b) })
	s.mu.Lock()
	s.gen++
	gen := s.gen
	s.cmd, s.wq, s.done, s.stdin = cmd, wq, done, stdin
	s.pid = cmd.Process.Pid
	s.stopping = false
	s.pending = map[string]chan rpcMsg{}
	s.clients = map[int]*client{}
	s.docs = map[string]int{}
	s.diags = map[string]json.RawMessage{}
	s.initResult = nil
	s.mu.Unlock()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := bufio.NewReaderSize(stdout, 64<<10)
		for {
			b, err := ReadMessage(r)
			if err != nil {
				return
			}
			s.dispatch(b)
		}
	}()
	go func() {
		<-readerDone
		cmd.Wait()
		close(done)
		s.processExited(gen)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), s.m.initTimeout)
	defer cancel()
	params := map[string]any{
		"processId":  os.Getpid(),
		"clientInfo": map[string]any{"name": "harness"},
		"rootUri":    fileURI(s.root), "rootPath": s.root,
		"workspaceFolders": []map[string]any{{"uri": fileURI(s.root), "name": baseName(s.root)}},
		"capabilities":     clientCaps(),
	}
	if len(s.cfg.InitOptions) > 0 {
		params["initializationOptions"] = s.cfg.InitOptions
	}
	res, err := s.request(ctx, "initialize", params)
	if err != nil {
		s.killNow()
		<-done
		return fmt.Errorf("lsp: initialize %s: %w", s.cfg.ID, err)
	}
	s.mu.Lock()
	s.initResult = res.Result
	if len(s.initResult) == 0 {
		s.initResult = json.RawMessage("null")
	}
	s.state = stRunning
	s.mu.Unlock()
	s.sendNotification("initialized", map[string]any{})
	return nil
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (s *server) killNow() {
	s.mu.Lock()
	cmd := s.cmd
	s.stopping = true
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func (s *server) writeRaw(b []byte) {
	s.mu.Lock()
	q := s.wq
	s.mu.Unlock()
	if q != nil {
		q.push(b)
	}
}

func (s *server) sendNotification(method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	s.writeRaw(b)
}

// request sends a Go-originated request and waits for the response.
func (s *server) request(ctx context.Context, method string, params any) (rpcMsg, error) {
	s.mu.Lock()
	s.nextGo++
	id := "go:" + strconv.Itoa(s.nextGo)
	ch := make(chan rpcMsg, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	s.writeRaw(b)
	select {
	case r := <-ch:
		if len(r.Error) > 0 {
			return r, fmt.Errorf("%s", r.Error)
		}
		return r, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return rpcMsg{}, ctx.Err()
	case <-s.done:
		return rpcMsg{}, errors.New("process exited")
	}
}

func (s *server) dispatch(b []byte) {
	var m rpcMsg
	if json.Unmarshal(b, &m) != nil {
		return
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method != "" && hasID: // server -> client request: answer in Go
		var result any
		switch m.Method {
		case "workspace/configuration":
			var p struct{ Items []json.RawMessage }
			json.Unmarshal(m.Params, &p)
			result = make([]any, len(p.Items))
		case "workspace/workspaceFolders":
			result = []map[string]any{{"uri": fileURI(s.root), "name": baseName(s.root)}}
		case "workspace/applyEdit":
			result = map[string]any{"applied": false}
		default:
			result = nil
		}
		resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		s.writeRaw(resp)
	case m.Method != "": // notification
		if m.Method == "textDocument/publishDiagnostics" {
			var p struct {
				URI string `json:"uri"`
			}
			if json.Unmarshal(m.Params, &p) == nil && p.URI != "" {
				s.mu.Lock()
				var d struct{ Diagnostics []json.RawMessage }
				json.Unmarshal(m.Params, &d)
				if len(d.Diagnostics) == 0 {
					delete(s.diags, p.URI)
				} else {
					s.diags[p.URI] = append([]byte(nil), b...)
				}
				s.mu.Unlock()
			}
		}
		s.broadcast(b)
	case hasID: // response
		var id string
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		if strings.HasPrefix(id, "go:") {
			s.mu.Lock()
			ch := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			return
		}
		if rest, ok := strings.CutPrefix(id, "c"); ok {
			num, orig, ok := strings.Cut(rest, ":")
			n, err := strconv.Atoi(num)
			if !ok || err != nil {
				return
			}
			s.mu.Lock()
			c := s.clients[n]
			s.mu.Unlock()
			if c != nil {
				if out, err := setID(b, json.RawMessage(orig)); err == nil {
					c.out.push(out)
				}
			}
		}
	}
}

func setID(raw []byte, id json.RawMessage) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["id"] = id
	return json.Marshal(m)
}

func (s *server) broadcast(b []byte) {
	s.mu.Lock()
	cs := make([]*client, 0, len(s.clients))
	for _, c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		c.out.push(b)
	}
}

// Attach binds a bus channel to the running server. It never blocks.
func (m *Manager) Attach(key string, conn Conn) error {
	m.mu.Lock()
	s := m.servers[key]
	m.mu.Unlock()
	if s == nil {
		return ErrNotRunning
	}
	s.mu.Lock()
	if s.state != stRunning {
		s.mu.Unlock()
		return ErrNotRunning
	}
	s.nextClient++
	c := &client{id: s.nextClient, conn: conn, docs: map[string]bool{}, gen: s.gen}
	c.out = newQueue(func(b []byte) { conn.SendJSON(json.RawMessage(b)) })
	s.clients[c.id] = c
	s.mu.Unlock()
	conn.OnJSON(func(raw json.RawMessage) { s.fromClient(c, raw) })
	conn.OnData(func(b []byte) { s.fromClient(c, json.RawMessage(append([]byte(nil), b...))) })
	conn.OnClose(func() { s.detach(c) })
	return nil
}

func (s *server) reply(c *client, id json.RawMessage, result json.RawMessage) {
	if len(result) == 0 {
		result = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	c.out.push(b)
}

func (s *server) fromClient(c *client, raw json.RawMessage) {
	var m rpcMsg
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method == "initialize" && hasID:
		s.mu.Lock()
		res := s.initResult
		s.mu.Unlock()
		s.reply(c, m.ID, res)
		return
	case m.Method == "shutdown" && hasID:
		s.reply(c, m.ID, nil)
		return
	case m.Method == "initialized" || m.Method == "exit":
		return
	case m.Method == "textDocument/didOpen":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		json.Unmarshal(m.Params, &p)
		uri := p.TextDocument.URI
		s.mu.Lock()
		if c.docs[uri] {
			s.mu.Unlock()
			return
		}
		c.docs[uri] = true
		s.docs[uri]++
		first := s.docs[uri] == 1
		cached := s.diags[uri]
		s.mu.Unlock()
		if first {
			s.writeRaw(raw)
		} else if cached != nil {
			c.out.push(cached)
		}
		return
	case m.Method == "textDocument/didClose":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		json.Unmarshal(m.Params, &p)
		uri := p.TextDocument.URI
		s.mu.Lock()
		if !c.docs[uri] {
			s.mu.Unlock()
			return
		}
		delete(c.docs, uri)
		s.docs[uri]--
		last := s.docs[uri] <= 0
		if last {
			delete(s.docs, uri)
			delete(s.diags, uri)
		}
		s.mu.Unlock()
		if last {
			s.writeRaw(raw)
		}
		return
	case m.Method == "$/cancelRequest":
		var p map[string]json.RawMessage
		if json.Unmarshal(m.Params, &p) == nil && p["id"] != nil {
			p["id"] = mustJSON(clientID(c, p["id"]))
			np, _ := json.Marshal(p)
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": m.Method, "params": json.RawMessage(np)})
			s.writeRaw(b)
		}
		return
	case m.Method != "" && hasID:
		out, err := setID(raw, mustJSON(clientID(c, m.ID)))
		if err == nil {
			s.writeRaw(out)
		}
		return
	case m.Method == "": // client responses: we answer server requests ourselves
		return
	}
	s.writeRaw(raw)
}

func clientID(c *client, orig json.RawMessage) string {
	return "c" + strconv.Itoa(c.id) + ":" + string(bytes.TrimSpace(orig))
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// detach removes a client: closes leftover docs and drops its reference.
func (s *server) detach(c *client) {
	s.mu.Lock()
	if c.closed {
		s.mu.Unlock()
		return
	}
	c.closed = true
	delete(s.clients, c.id)
	var closes [][]byte
	if c.gen == s.gen && s.state == stRunning {
		for uri := range c.docs {
			s.docs[uri]--
			if s.docs[uri] <= 0 {
				delete(s.docs, uri)
				delete(s.diags, uri)
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didClose",
					"params": map[string]any{"textDocument": map[string]any{"uri": uri}}})
				closes = append(closes, b)
			}
		}
		s.releaseLocked()
	}
	c.docs = map[string]bool{}
	q := s.wq
	s.mu.Unlock()
	for _, b := range closes {
		q.push(b)
	}
	c.out.close()
	s.m.notify()
}

func (s *server) releaseLocked() {
	if s.refs > 0 {
		s.refs--
	}
	if s.refs == 0 && s.state == stRunning {
		s.armIdleLocked()
	}
}

func (s *server) cancelIdleLocked() {
	s.idleTok++
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
}

func (s *server) armIdleLocked() {
	s.cancelIdleLocked()
	tok := s.idleTok
	s.idleTimer = time.AfterFunc(s.m.idleTTL, func() { s.idleExpire(tok) })
}

func (s *server) idleExpire(tok int) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	if tok != s.idleTok || s.refs != 0 || s.state != stRunning {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.shutdown()
	s.m.notify()
}

// shutdown performs LSP shutdown+exit, killing after killGrace. startMu held.
func (s *server) shutdown() {
	s.mu.Lock()
	s.stopping = true
	s.cancelIdleLocked()
	cmd, done, wq := s.cmd, s.done, s.wq
	cs := make([]*client, 0, len(s.clients))
	for _, c := range s.clients {
		cs = append(cs, c)
	}
	s.clients = map[int]*client{}
	s.mu.Unlock()
	for _, c := range cs {
		c := c
		c.out.finish(func() { c.conn.Close() })
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.m.killGrace)
	s.request(ctx, "shutdown", nil)
	cancel()
	s.sendNotification("exit", nil)
	stdin := s.stdin
	wq.finish(func() { stdin.Close() })
	select {
	case <-done:
	case <-time.After(s.m.killGrace):
		cmd.Process.Kill()
		<-done
	}
	s.mu.Lock()
	s.state = stStopped
	s.pid = 0
	s.refs = 0
	s.gen++
	s.mu.Unlock()
}

// processExited runs when the process is gone.
func (s *server) processExited(gen int) {
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return
	}
	wq := s.wq
	if s.stopping {
		s.mu.Unlock()
		wq.close()
		return
	}
	s.state = stCrashed
	s.pid = 0
	s.refs = 0
	s.gen++
	s.cancelIdleLocked()
	cs := make([]*client, 0, len(s.clients))
	for _, c := range s.clients {
		cs = append(cs, c)
	}
	s.clients = map[int]*client{}
	key := s.key
	s.mu.Unlock()
	wq.close()
	msg, _ := json.Marshal(map[string]any{"type": "lsp_status", "key": key, "state": stCrashed})
	for _, c := range cs {
		c := c
		c.out.push(msg)
		c.out.finish(func() { c.conn.Close() })
	}
	s.m.notify()
}
