package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestFrameRoundTrip(t *testing.T) {
	f := Frame{Kind: KindData, Chan: 0x01020304, Payload: []byte("hello")}
	b := f.Encode()
	if !bytes.Equal(b[:5], []byte{2, 1, 2, 3, 4}) {
		t.Fatalf("header %v", b[:5])
	}
	g, err := Decode(b)
	if err != nil || g.Kind != KindData || g.Chan != 0x01020304 || string(g.Payload) != "hello" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := Decode([]byte{1, 2}); err != ErrShortFrame {
		t.Fatal("want short frame")
	}
	n, _ := ParseAck(AckPayload(123456))
	if n != 123456 {
		t.Fatal(n)
	}
}

func TestCreditBlocksAndReleases(t *testing.T) {
	c := NewCredit(100)
	ctx := context.Background()
	if err := c.Acquire(ctx, 60, 100); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Acquire(ctx, 60, 100) }()
	select {
	case <-done:
		t.Fatal("should block")
	case <-time.After(50 * time.Millisecond):
	}
	c.Release(60)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("not released")
	}
	if c.Available() != 40 {
		t.Fatalf("avail %d", c.Available())
	}
}

func TestCreditOversizeAndCancelAndClose(t *testing.T) {
	c := NewCredit(100)
	// oversize request passes when window is full
	if err := c.Acquire(context.Background(), 500, 100); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := c.Acquire(ctx, 10, 100); err == nil {
		t.Fatal("want ctx error")
	}
	go func() { time.Sleep(20 * time.Millisecond); c.Close() }()
	if err := c.Acquire(context.Background(), 10, 100); err != ErrClosed {
		t.Fatalf("got %v", err)
	}
}

func dial(t *testing.T, b *Bus) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/bus?token=%s", b.Port(), b.Token()), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(1 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func read(t *testing.T, ws *websocket.Conn) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, d, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(d)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func write(t *testing.T, ws *websocket.Conn, f Frame) {
	t.Helper()
	if err := ws.Write(context.Background(), websocket.MessageBinary, f.Encode()); err != nil {
		t.Fatal(err)
	}
}

func startBus(t *testing.T) *Bus {
	t.Helper()
	b := New()
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestTokenAndLoopback(t *testing.T) {
	b := startBus(t)
	if len(b.Token()) != 64 || b.Port() == 0 {
		t.Fatal("token/port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/bus?token=bad", b.Port()), nil)
	if err == nil {
		t.Fatal("bad token accepted")
	}
}

func TestServerOpenAndJSON(t *testing.T) {
	b := startBus(t)
	ch := b.Open("session/1") // before any client: announced on connect
	ws := dial(t, b)
	f := read(t, ws)
	if f.Kind != KindOpen || string(f.Payload) != "session/1" || f.Chan != ch.ID() {
		t.Fatalf("%+v", f)
	}
	if err := ch.SendJSON(map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	f = read(t, ws)
	if f.Kind != KindJSON || string(f.Payload) != `{"a":1}` {
		t.Fatalf("%+v", f)
	}
	ch.Close()
	f = read(t, ws)
	if f.Kind != KindClose {
		t.Fatalf("%+v", f)
	}
}

func TestClientOpenHandlerAndAck(t *testing.T) {
	b := startBus(t)
	got := make(chan []byte, 1)
	gotJSON := make(chan json.RawMessage, 1)
	chs := make(chan *Channel, 1)
	b.Handle("pty/", func(ch *Channel) {
		ch.OnData(func(p []byte) { got <- append([]byte(nil), p...) })
		ch.OnJSON(func(p json.RawMessage) { gotJSON <- append(json.RawMessage(nil), p...) })
		chs <- ch
	})
	ws := dial(t, b)
	write(t, ws, Frame{Kind: KindOpen, Chan: 1, Payload: []byte("pty/abc")})
	write(t, ws, Frame{Kind: KindData, Chan: 1, Payload: []byte("xyz")})
	if string(<-got) != "xyz" {
		t.Fatal("data")
	}
	f := read(t, ws)
	if f.Kind != KindAck || f.Chan != 1 {
		t.Fatalf("%+v", f)
	}
	if n, _ := ParseAck(f.Payload); n != 3 {
		t.Fatal(n)
	}
	write(t, ws, Frame{Kind: KindJSON, Chan: 1, Payload: []byte(`{"k":true}`)})
	if string(<-gotJSON) != `{"k":true}` {
		t.Fatal("json")
	}
	read(t, ws) // ack
	ch := <-chs
	if ch.Name() != "pty/abc" {
		t.Fatal(ch.Name())
	}
	// unknown prefix gets closed
	write(t, ws, Frame{Kind: KindOpen, Chan: 3, Payload: []byte("nope/1")})
	f = read(t, ws)
	if f.Kind != KindClose || f.Chan != 3 {
		t.Fatalf("%+v", f)
	}
}

func TestBackpressureEndToEnd(t *testing.T) {
	b := startBus(t)
	ch := b.Open("pty/x")
	ws := dial(t, b)
	read(t, ws) // open

	chunk := make([]byte, 64*1024)
	sent := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < 8; i++ { // 512KB total, window is 256KB
			if err := ch.SendBytes(chunk); err != nil {
				break
			}
			n++
			sent <- n
		}
	}()
	// Without acks only 4 chunks fit.
	var last int
	deadline := time.After(300 * time.Millisecond)
loop:
	for {
		select {
		case last = <-sent:
		case <-deadline:
			break loop
		}
	}
	if last != 4 {
		t.Fatalf("sent %d chunks without acks, want 4", last)
	}
	// Drain 4 frames and ack them; the rest should flow.
	for i := 0; i < 4; i++ {
		f := read(t, ws)
		if f.Kind != KindData {
			t.Fatalf("%+v", f)
		}
	}
	write(t, ws, Frame{Kind: KindAck, Chan: ch.ID(), Payload: AckPayload(4 * 64 * 1024)})
	deadline = time.After(2 * time.Second)
	for last < 8 {
		select {
		case last = <-sent:
		case <-deadline:
			t.Fatalf("stalled at %d", last)
		}
	}
}

func TestChannelClosesWhenConnDrops(t *testing.T) {
	b := startBus(t)
	closed := make(chan struct{})
	b.Handle("a/", func(ch *Channel) { ch.OnClose(func() { close(closed) }) })
	ws := dial(t, b)
	write(t, ws, Frame{Kind: KindOpen, Chan: 1, Payload: []byte("a/1")})
	time.Sleep(50 * time.Millisecond)
	ws.Close(websocket.StatusNormalClosure, "")
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("no close")
	}
}

func TestTapWritesNDJSON(t *testing.T) {
	dir := t.TempDir()
	tap := NewTap(dir)
	tap.Log("out", Frame{Kind: KindJSON, Chan: 2, Payload: []byte(`{"x":1}`)})
	tap.Log("in", Frame{Kind: KindData, Chan: 1, Payload: []byte{0xff, 0xfe}})
	tap.Log("in", Frame{Kind: KindAck, Chan: 1, Payload: AckPayload(7)})
	p := tap.Path()
	tap.Close()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(lines[1], `"b64"`) || !strings.Contains(lines[2], `"payload":"7"`) {
		t.Fatalf("%v", lines)
	}
}
