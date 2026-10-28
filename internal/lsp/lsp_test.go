package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("LSP_FAKE") == "1" {
		fakeServer()
		return
	}
	os.Exit(m.Run())
}

func fakeServer() {
	logf := func(s string) {
		if p := os.Getenv("LSP_FAKE_LOG"); p != "" {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			f.WriteString(s + "\n")
			f.Close()
		}
	}
	r := bufio.NewReader(os.Stdin)
	send := func(v any) { b, _ := json.Marshal(v); WriteMessage(os.Stdout, b) }
	for {
		b, err := ReadMessage(r)
		if err != nil {
			return
		}
		var m rpcMsg
		json.Unmarshal(b, &m)
		switch m.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"capabilities": map[string]any{"hoverProvider": true}}})
		case "initialized":
			logf("initialized")
			send(map[string]any{"jsonrpc": "2.0", "id": 900, "method": "workspace/configuration", "params": map[string]any{"items": []any{1, 2}}})
		case "textDocument/didOpen":
			var p struct{ TextDocument struct{ URI string } }
			json.Unmarshal(m.Params, &p)
			logf("didOpen " + p.TextDocument.URI)
			send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
				"params": map[string]any{"uri": p.TextDocument.URI, "diagnostics": []any{map[string]any{"message": "boom"}}}})
		case "textDocument/didClose":
			var p struct{ TextDocument struct{ URI string } }
			json.Unmarshal(m.Params, &p)
			logf("didClose " + p.TextDocument.URI)
		case "textDocument/hover":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"contents": "hover " + string(m.ID)}})
		case "crash":
			os.Exit(3)
		case "shutdown":
			logf("shutdown")
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": nil})
		case "exit":
			logf("exit")
			os.Exit(0)
		default:
			if m.Method == "" && string(m.ID) == "900" {
				logf("cfgresp " + string(m.Result))
			}
		}
	}
}

func fakeMgr(t *testing.T, ttl time.Duration) (*Manager, string) {
	t.Helper()
	t.Setenv("LSP_FAKE", "1")
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("LSP_FAKE_LOG", log)
	m := NewManager(Options{IdleTTL: ttl, KillGrace: time.Second, InitTimeout: 10 * time.Second,
		Servers: []ServerConfig{{ID: "fake", Languages: []string{"fakelang"}, Command: os.Args[0]}}})
	t.Cleanup(m.Close)
	return m, log
}

type fakeConn struct {
	got     chan json.RawMessage
	mu      sync.Mutex
	onJSON  func(json.RawMessage)
	onClose func()
	closed  bool
}

func newConn() *fakeConn { return &fakeConn{got: make(chan json.RawMessage, 100)} }
func (c *fakeConn) SendJSON(v any) error {
	b, _ := json.Marshal(v)
	c.got <- b
	return nil
}
func (c *fakeConn) OnData(func([]byte))            {}
func (c *fakeConn) OnJSON(f func(json.RawMessage)) { c.onJSON = f }
func (c *fakeConn) OnClose(f func())               { c.onClose = f }
func (c *fakeConn) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	if c.onClose != nil {
		c.onClose()
	}
}
func (c *fakeConn) send(s string) { c.onJSON(json.RawMessage(s)) }
func (c *fakeConn) recv(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	select {
	case b := <-c.got:
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}
	return nil
}
func (c *fakeConn) none(t *testing.T) {
	t.Helper()
	select {
	case b := <-c.got:
		t.Fatalf("unexpected message %s", b)
	case <-time.After(150 * time.Millisecond):
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func logHas(log, s string) bool {
	b, _ := os.ReadFile(log)
	return strings.Contains(string(b), s)
}

func state(m *Manager) string {
	st := m.Status()
	if len(st) == 0 {
		return ""
	}
	return st[0].State
}

func TestFraming(t *testing.T) {
	var buf bytes.Buffer
	WriteMessage(&buf, []byte(`{"a":1}`))
	WriteMessage(&buf, []byte(`{"b":2}`))
	r := bufio.NewReader(&buf)
	for _, want := range []string{`{"a":1}`, `{"b":2}`} {
		b, err := ReadMessage(r)
		if err != nil || string(b) != want {
			t.Fatalf("got %q %v", b, err)
		}
	}
	if _, err := ReadMessage(r); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) { return o.r.Read(p[:1]) }

func TestFramingEdges(t *testing.T) {
	// split reads, extra headers, lowercase name
	in := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\ncontent-length: 5\r\n\r\nhello"
	b, err := ReadMessage(bufio.NewReaderSize(oneByte{strings.NewReader(in)}, 16))
	if err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	// zero length
	b, err = ReadMessage(bufio.NewReader(strings.NewReader("Content-Length: 0\r\n\r\n")))
	if err != nil || len(b) != 0 {
		t.Fatalf("%q %v", b, err)
	}
	for name, in := range map[string]string{
		"missing":   "X: 1\r\n\r\n{}",
		"negative":  "Content-Length: -1\r\n\r\n",
		"nonnum":    "Content-Length: abc\r\n\r\n",
		"huge":      "Content-Length: 99999999999\r\n\r\n",
		"malformed": "garbage\r\n\r\n",
	} {
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	for name, in := range map[string]string{
		"short body":   "Content-Length: 10\r\n\r\nabc",
		"short header": "Content-Length: 3\r\n",
	} {
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader(in))); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// bare LF line endings tolerated
	b, err = ReadMessage(bufio.NewReader(strings.NewReader("Content-Length: 2\n\nhi")))
	if err != nil || string(b) != "hi" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestMerge(t *testing.T) {
	out := MergeConfigs(DefaultServers(), []ServerConfig{{ID: "gopls", Command: "/x/gopls"}, {ID: "new", Languages: []string{"zig"}, Command: "zls"}})
	if len(out) != len(DefaultServers())+1 || out[0].Command != "/x/gopls" || out[0].Languages[0] != "go" {
		t.Fatalf("%+v", out)
	}
}

func TestNotInstalled(t *testing.T) {
	m := NewManager(Options{Servers: []ServerConfig{{ID: "nope", Languages: []string{"nopelang"}, Command: "definitely-not-a-real-lsp-binary"}}})
	_, err := m.Acquire(t.TempDir(), "nopelang")
	var ni *ErrNotInstalled
	if !errors.As(err, &ni) || ni.ID != "nope" || ni.Hint == "" {
		t.Fatalf("got %v", err)
	}
	m2 := NewManager(Options{Servers: []ServerConfig{{ID: "gopls", Command: "definitely-not-gopls"}}})
	_, err = m2.Acquire(t.TempDir(), "go")
	if !errors.As(err, &ni) || !strings.Contains(ni.Hint, "go install golang.org/x/tools/gopls@latest") {
		t.Fatalf("got %v", err)
	}
	if _, err := m.Acquire(t.TempDir(), "klingon"); !errors.Is(err, ErrNoServer) {
		t.Fatal(err)
	}
}

func TestCachedInitializeAndRelay(t *testing.T) {
	m, log := fakeMgr(t, time.Minute)
	root := t.TempDir()
	key, err := m.Acquire(root, "fakelang")
	if err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if len(st) != 1 || st[0].State != "running" || st[0].Refcount != 1 || st[0].Pid == 0 || st[0].Root != root {
		t.Fatalf("%+v", st)
	}
	c := newConn()
	if err := m.Attach(key, c); err != nil {
		t.Fatal(err)
	}
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	r := c.recv(t)
	if string(r["id"]) != "1" || !strings.Contains(string(r["result"]), "hoverProvider") {
		t.Fatalf("%v", r)
	}
	c.send(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)
	c.send(`{"jsonrpc":"2.0","id":"abc","method":"textDocument/hover","params":{}}`)
	r = c.recv(t)
	if string(r["id"]) != `"abc"` || !strings.Contains(string(r["result"]), `hover \"c1:\\\"abc\\\"\"`) {
		t.Fatalf("%s %s", r["id"], r["result"])
	}
	// Go answered workspace/configuration; "initialized" reached server exactly once (from Go).
	waitFor(t, "cfgresp", func() bool { return logHas(log, "cfgresp [null,null]") })
	b, _ := os.ReadFile(log)
	if strings.Count(string(b), "initialized") != 1 {
		t.Fatalf("initialized forwarded: %s", b)
	}
	c.none(t)
}

func TestTwoClientsIDsAndDiagnostics(t *testing.T) {
	m, log := fakeMgr(t, time.Minute)
	root := t.TempDir()
	key, _ := m.Acquire(root, "fakelang")
	m.Acquire(root, "fakelang")
	a, b := newConn(), newConn()
	m.Attach(key, a)
	m.Attach(key, b)
	// same id from both clients, routed separately
	a.send(`{"jsonrpc":"2.0","id":7,"method":"textDocument/hover","params":{}}`)
	b.send(`{"jsonrpc":"2.0","id":7,"method":"textDocument/hover","params":{}}`)
	ra, rb := a.recv(t), b.recv(t)
	if string(ra["id"]) != "7" || string(rb["id"]) != "7" {
		t.Fatalf("ids %s %s", ra["id"], rb["id"])
	}
	if string(ra["result"]) == string(rb["result"]) {
		t.Fatalf("server saw identical ids: %s", ra["result"])
	}
	a.none(t)
	b.none(t)
	// diagnostics broadcast
	a.send(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///x.go","languageId":"go","version":1,"text":""}}}`)
	for _, c := range []*fakeConn{a, b} {
		r := c.recv(t)
		if string(r["method"]) != `"textDocument/publishDiagnostics"` {
			t.Fatalf("%v", r)
		}
	}
	// second client opening the same doc: not re-sent to server, cached diags replayed
	b.send(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///x.go","languageId":"go","version":1,"text":""}}}`)
	if r := b.recv(t); string(r["method"]) != `"textDocument/publishDiagnostics"` {
		t.Fatalf("%v", r)
	}
	a.none(t)
	data, _ := os.ReadFile(log)
	if strings.Count(string(data), "didOpen") != 1 {
		t.Fatalf("log: %s", data)
	}
	// a disconnects: doc still held by b, no didClose
	a.Close()
	time.Sleep(150 * time.Millisecond)
	if logHas(log, "didClose") {
		t.Fatal("premature didClose")
	}
	b.Close()
	waitFor(t, "didClose", func() bool { return logHas(log, "didClose file:///x.go") })
	if st := m.Status()[0]; st.Refcount != 0 || st.State != "idle" {
		t.Fatalf("%+v", st)
	}
}

func TestLeftoverDidCloseOnDisconnect(t *testing.T) {
	m, log := fakeMgr(t, time.Minute)
	key, _ := m.Acquire(t.TempDir(), "fakelang")
	c := newConn()
	m.Attach(key, c)
	c.send(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///a.go"}}}`)
	c.send(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///b.go"}}}`)
	c.send(`{"jsonrpc":"2.0","method":"textDocument/didClose","params":{"textDocument":{"uri":"file:///b.go"}}}`)
	waitFor(t, "didClose b", func() bool { return logHas(log, "didClose file:///b.go") })
	c.Close()
	waitFor(t, "didClose a", func() bool { return logHas(log, "didClose file:///a.go") })
	b, _ := os.ReadFile(log)
	if strings.Count(string(b), "didClose file:///b.go") != 1 {
		t.Fatalf("%s", b)
	}
}

func TestIdleShutdownAndRestart(t *testing.T) {
	m, log := fakeMgr(t, 150*time.Millisecond)
	root := t.TempDir()
	var mu sync.Mutex
	var states []string
	m.Subscribe(func(s []ServerStatus) {
		mu.Lock()
		if len(s) > 0 {
			states = append(states, s[0].State)
		}
		mu.Unlock()
	})
	key, _ := m.Acquire(root, "fakelang")
	m.Release(key)
	if state(m) != "idle" {
		t.Fatalf("state %s", state(m))
	}
	waitFor(t, "stopped", func() bool { return state(m) == "stopped" })
	if !logHas(log, "shutdown") || !logHas(log, "exit") {
		t.Fatal("no shutdown/exit")
	}
	mu.Lock()
	if !strings.Contains(strings.Join(states, ","), "starting") {
		t.Fatalf("states %v", states)
	}
	mu.Unlock()
	if _, err := m.Acquire(root, "fakelang"); err != nil {
		t.Fatal(err)
	}
	if state(m) != "running" {
		t.Fatal(state(m))
	}
}

func TestTouchExtendsTTLAndNeverStarts(t *testing.T) {
	m, _ := fakeMgr(t, 400*time.Millisecond)
	m.SetServers([]ServerConfig{{ID: "gopls", Languages: []string{"go"}, Command: os.Args[0]}})
	root := t.TempDir()
	if m.Touch(root, filepath.Join(root, "a.go")) {
		t.Fatal("touch reported a server")
	}
	if len(m.Status()) != 0 {
		t.Fatal("touch started a server")
	}
	key, err := m.Acquire(root, "go")
	if err != nil {
		t.Fatal(err)
	}
	m.Release(key)
	start := time.Now()
	for time.Since(start) < 700*time.Millisecond {
		time.Sleep(100 * time.Millisecond)
		if !m.Touch(root, filepath.Join(root, "a.go")) {
			t.Fatal("touch missed")
		}
		if state(m) != "idle" {
			t.Fatalf("stopped early: %s", state(m))
		}
	}
	waitFor(t, "stopped after touches end", func() bool { return state(m) == "stopped" })
	if m.Touch(root, filepath.Join(root, "a.go")) {
		t.Fatal("touch on stopped server")
	}
}

func TestCrashAndRestart(t *testing.T) {
	m, _ := fakeMgr(t, time.Minute)
	root := t.TempDir()
	key, _ := m.Acquire(root, "fakelang")
	c := newConn()
	m.Attach(key, c)
	c.send(`{"jsonrpc":"2.0","method":"crash"}`)
	r := c.recv(t)
	if string(r["type"]) != `"lsp_status"` || string(r["state"]) != `"crashed"` {
		t.Fatalf("%v", r)
	}
	waitFor(t, "crashed", func() bool { return state(m) == "crashed" })
	waitFor(t, "conn closed", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed })
	key2, err := m.Acquire(root, "fakelang")
	if err != nil || key2 != key {
		t.Fatal(key2, err)
	}
	if st := m.Status()[0]; st.State != "running" || st.Refcount != 1 {
		t.Fatalf("%+v", st)
	}
	c2 := newConn()
	if err := m.Attach(key, c2); err != nil {
		t.Fatal(err)
	}
	c2.send(`{"jsonrpc":"2.0","id":1,"method":"textDocument/hover"}`)
	c2.recv(t)
}

func TestIntegrationGopls(t *testing.T) {
	if os.Getenv("HARNESS_IT_GOPLS") != "1" {
		t.Skip("set HARNESS_IT_GOPLS=1")
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, "go", "bin", "gopls")); err != nil {
		if _, err := exec.LookPath("gopls"); err != nil {
			t.Skip("gopls not installed")
		}
	}
	root := t.TempDir()
	src := "package main\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n\nfunc main() { _ = Add(1, 2) }\n"
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/it\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0o644)
	m := NewManager(Options{})
	t.Cleanup(m.Close)
	key, err := m.Acquire(root, "go")
	if err != nil {
		t.Fatal(err)
	}
	c := newConn()
	m.Attach(key, c)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if r := c.recv(t); !strings.Contains(string(r["result"]), "hoverProvider") {
		t.Fatalf("%s", r["result"])
	}
	uri := fileURI(filepath.Join(root, "main.go"))
	open, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "go", "version": 1, "text": src}}})
	c.send(string(open))
	hov, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/hover", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri}, "position": map[string]any{"line": 3, "character": 6}}})
	c.send(string(hov))
	deadline := time.After(60 * time.Second)
	for {
		select {
		case b := <-c.got:
			var msg rpcMsg
			json.Unmarshal(b, &msg)
			if string(msg.ID) == "2" {
				if !strings.Contains(string(msg.Result), "Add") {
					t.Fatalf("hover: %s", msg.Result)
				}
				return
			}
		case <-deadline:
			t.Fatal("no hover response")
		}
	}
}
