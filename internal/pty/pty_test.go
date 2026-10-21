package pty

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeConn struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	jsons   []string
	onData  func([]byte)
	onJSON  func(json.RawMessage)
	onClose func()
}

func (f *fakeConn) SendBytes(b []byte) error { f.mu.Lock(); f.buf.Write(b); f.mu.Unlock(); return nil }
func (f *fakeConn) SendJSON(v any) error {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	f.jsons = append(f.jsons, string(b))
	f.mu.Unlock()
	return nil
}
func (f *fakeConn) OnData(fn func([]byte))          { f.onData = fn }
func (f *fakeConn) OnJSON(fn func(json.RawMessage)) { f.onJSON = fn }
func (f *fakeConn) OnClose(fn func())               { f.onClose = fn }
func (f *fakeConn) Close()                          {}
func (f *fakeConn) String() string                  { f.mu.Lock(); defer f.mu.Unlock(); return f.buf.String() }
func (f *fakeConn) Jsons() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.jsons, "|")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestOutputReplayAndOSC7(t *testing.T) {
	m := NewManager()
	id, err := m.SpawnCmd("s", t.TempDir(), 80, 24, "shell", nil, "/bin/sh", "-c", `echo hi; printf "\033]7;file://h/tmp\007"; sleep 0.3`)
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeConn{}
	if err := m.Attach(id, a); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "output", func() bool { return strings.Contains(a.String(), "hi") })
	waitFor(t, "exit", func() bool { i, _ := m.Info(id); return i.Exited })
	waitFor(t, "exit event", func() bool { return strings.Contains(a.Jsons(), `"exit"`) })
	in, _ := m.Info(id)
	if in.Cwd != "/tmp" || in.ExitCode != 0 {
		t.Fatalf("info %+v", in)
	}
	b := &fakeConn{}
	m.Attach(id, b)
	waitFor(t, "replay", func() bool { return strings.Contains(b.String(), "hi") })
	if got := m.List("s"); len(got) != 1 || len(m.List("other")) != 0 {
		t.Fatalf("list %v", got)
	}
	a.onClose()
	b.onClose()
	m.Kill(id)
	if _, err := m.Info(id); err == nil {
		t.Fatal("expected removal after killing exited pty")
	}
}

func TestInputResizeKill(t *testing.T) {
	m := NewManager()
	id, err := m.SpawnCmd("s", "", 80, 24, "shell", nil, "/bin/sh", "-c", `read x; echo got:$x; stty size; sleep 30`)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeConn{}
	m.Attach(id, c)
	c.onJSON(json.RawMessage(`{"type":"resize","cols":100,"rows":30}`))
	waitFor(t, "resize", func() bool { i, _ := m.Info(id); return i.Cols == 100 && i.Rows == 30 })
	time.Sleep(100 * time.Millisecond)
	c.onData([]byte("abc\n"))
	waitFor(t, "echo", func() bool { return strings.Contains(c.String(), "got:abc") })
	waitFor(t, "size", func() bool { return strings.Contains(c.String(), "30 100") })
	if err := m.Kill(id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "killed", func() bool { i, _ := m.Info(id); return i.Exited })
	if i, _ := m.Info(id); i.ExitCode == 0 {
		t.Fatalf("expected nonzero exit, got %+v", i)
	}
}

func TestSpawnShell(t *testing.T) {
	m := NewManager()
	id, err := m.Spawn("s", "/tmp", 80, 24, "shell", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeConn{}
	m.Attach(id, c)
	c.onData([]byte("echo $TERM-$COLORTERM\n"))
	waitFor(t, "env", func() bool { return strings.Contains(c.String(), "xterm-256color-truecolor") })
	m.KillAll()
	if i, _ := m.Info(id); !i.Exited {
		t.Fatal("not exited")
	}
}
