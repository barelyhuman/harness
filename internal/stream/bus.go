package stream

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

var ErrClosed = errors.New("stream: closed")

// Handler is called (synchronously on the connection's read loop, so it must
// not block) when a client opens a channel whose name matches a prefix.
type Handler func(ch *Channel)

// Bus is the loopback WebSocket hub.
type Bus struct {
	token string
	tap   *Tap

	mu       sync.Mutex
	ln       net.Listener
	srv      *http.Server
	conns    map[*conn]struct{}
	chans    map[uint32]*Channel // server-initiated, even ids
	handlers map[string]Handler
	nextID   uint32
	closed   bool
}

// New creates a bus with a fresh 32-byte hex token. Call Start to listen.
func New() *Bus {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return &Bus{
		token:    hex.EncodeToString(b),
		conns:    map[*conn]struct{}{},
		chans:    map[uint32]*Channel{},
		handlers: map[string]Handler{},
		tap:      NewTapFromEnv(),
	}
}

// Token returns the auth token (64 hex chars = 32 bytes).
func (b *Bus) Token() string { return b.token }

// Handle registers h for client-initiated opens whose name has the given
// prefix (e.g. "pty/"). The longest matching prefix wins.
func (b *Bus) Handle(prefix string, h Handler) {
	b.mu.Lock()
	b.handlers[prefix] = h
	b.mu.Unlock()
}

// Start listens on 127.0.0.1:<random port> and serves /bus.
func (b *Bus) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/bus", b.serveWS)
	b.mu.Lock()
	b.ln = ln
	b.srv = &http.Server{Handler: mux}
	srv := b.srv
	b.mu.Unlock()
	go srv.Serve(ln)
	return nil
}

// Port returns the listening port (0 before Start).
func (b *Bus) Port() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln == nil {
		return 0
	}
	return b.ln.Addr().(*net.TCPAddr).Port
}

// Close shuts down the listener and all connections.
func (b *Bus) Close() error {
	b.mu.Lock()
	b.closed = true
	srv := b.srv
	conns := make([]*conn, 0, len(b.conns))
	for c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	for _, c := range conns {
		c.ws.Close(websocket.StatusGoingAway, "bus closing")
	}
	b.tap.Close()
	if srv != nil {
		return srv.Close()
	}
	return nil
}

// Open creates a server-initiated channel and announces it to all clients.
func (b *Bus) Open(name string) *Channel {
	b.mu.Lock()
	b.nextID += 2
	id := b.nextID
	ch := newChannel(b, id, name, nil)
	b.chans[id] = ch
	conns := b.connList()
	b.mu.Unlock()
	for _, c := range conns {
		c.send(Frame{Kind: KindOpen, Chan: id, Payload: []byte(name)})
	}
	return ch
}

func (b *Bus) connList() []*conn {
	out := make([]*conn, 0, len(b.conns))
	for c := range b.conns {
		out = append(out, c)
	}
	return out
}

func (b *Bus) serveWS(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(b.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// The webview origin varies (wails://, http://wails.localhost, vite dev);
	// the random token is the credential, so skip the origin check.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(16 << 20)
	c := &conn{bus: b, ws: ws, chans: map[uint32]*Channel{}, credits: map[uint32]*Credit{}}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		ws.Close(websocket.StatusGoingAway, "closed")
		return
	}
	b.conns[c] = struct{}{}
	// Announce live server-initiated channels, in id order.
	ids := make([]int, 0, len(b.chans))
	for id := range b.chans {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	announce := make([]*Channel, 0, len(ids))
	for _, id := range ids {
		announce = append(announce, b.chans[uint32(id)])
	}
	b.mu.Unlock()
	for _, ch := range announce {
		c.send(Frame{Kind: KindOpen, Chan: ch.id, Payload: []byte(ch.name)})
	}
	c.readLoop(r.Context())
	b.mu.Lock()
	delete(b.conns, c)
	b.mu.Unlock()
	c.shutdown()
}

func (b *Bus) handlerFor(name string) Handler {
	b.mu.Lock()
	defer b.mu.Unlock()
	best := ""
	var h Handler
	for p, hh := range b.handlers {
		if strings.HasPrefix(name, p) && (h == nil || len(p) > len(best)) {
			best, h = p, hh
		}
	}
	return h
}

// conn is one WebSocket client.
type conn struct {
	bus *Bus
	ws  *websocket.Conn

	mu      sync.Mutex
	chans   map[uint32]*Channel // client-initiated (odd ids)
	credits map[uint32]*Credit  // outbound credit per channel id
	dead    bool
}

func (c *conn) send(f Frame) error {
	c.bus.tap.Log("out", f)
	return c.ws.Write(context.Background(), websocket.MessageBinary, f.Encode())
}

func (c *conn) credit(id uint32) *Credit {
	c.mu.Lock()
	defer c.mu.Unlock()
	cr := c.credits[id]
	if cr == nil {
		cr = NewCredit(Window)
		if c.dead {
			cr.Close()
		}
		c.credits[id] = cr
	}
	return cr
}

func (c *conn) lookup(id uint32) *Channel {
	if id%2 == 0 {
		c.bus.mu.Lock()
		defer c.bus.mu.Unlock()
		return c.bus.chans[id]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chans[id]
}

func (c *conn) readLoop(ctx context.Context) {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		f, err := Decode(data)
		if err != nil {
			continue
		}
		c.bus.tap.Log("in", f)
		c.handle(f)
	}
}

func (c *conn) handle(f Frame) {
	switch f.Kind {
	case KindOpen:
		if f.Chan%2 == 0 {
			return // clients may only open odd ids
		}
		name := string(f.Payload)
		h := c.bus.handlerFor(name)
		if h == nil {
			c.send(Frame{Kind: KindClose, Chan: f.Chan})
			return
		}
		ch := newChannel(c.bus, f.Chan, name, c)
		c.mu.Lock()
		c.chans[f.Chan] = ch
		c.mu.Unlock()
		h(ch)
	case KindAck:
		n, err := ParseAck(f.Payload)
		if err == nil {
			c.credit(f.Chan).Release(int(n))
		}
	case KindData, KindJSON:
		ch := c.lookup(f.Chan)
		if ch == nil {
			return
		}
		ch.deliver(f)
		if len(f.Payload) > 0 {
			c.send(Frame{Kind: KindAck, Chan: f.Chan, Payload: AckPayload(uint32(len(f.Payload)))})
		}
	case KindClose:
		if f.Chan%2 == 1 {
			c.mu.Lock()
			ch := c.chans[f.Chan]
			delete(c.chans, f.Chan)
			delete(c.credits, f.Chan)
			c.mu.Unlock()
			if ch != nil {
				ch.markClosed(false)
			}
		}
		// A client "close" of a server-initiated channel just detaches that
		// client's interest; the channel itself lives on.
	}
}

func (c *conn) shutdown() {
	c.mu.Lock()
	c.dead = true
	chans := c.chans
	c.chans = map[uint32]*Channel{}
	for _, cr := range c.credits {
		cr.Close()
	}
	c.mu.Unlock()
	for _, ch := range chans {
		ch.markClosed(false)
	}
}

// Channel is one logical stream on the bus.
type Channel struct {
	bus   *Bus
	id    uint32
	name  string
	owner *conn // non-nil for client-initiated channels

	mu       sync.Mutex
	onData   func([]byte)
	onJSON   func(json.RawMessage)
	onClose  func()
	closed   atomic.Bool
	closedCh chan struct{}
}

func newChannel(b *Bus, id uint32, name string, owner *conn) *Channel {
	return &Channel{bus: b, id: id, name: name, owner: owner, closedCh: make(chan struct{})}
}

func (ch *Channel) Name() string { return ch.name }
func (ch *Channel) ID() uint32   { return ch.id }

// OnData sets the handler for inbound binary data frames.
func (ch *Channel) OnData(fn func([]byte)) { ch.mu.Lock(); ch.onData = fn; ch.mu.Unlock() }

// OnJSON sets the handler for inbound JSON frames.
func (ch *Channel) OnJSON(fn func(json.RawMessage)) { ch.mu.Lock(); ch.onJSON = fn; ch.mu.Unlock() }

// OnClose sets a handler called once when the channel closes (by either side
// or because the owning connection dropped).
func (ch *Channel) OnClose(fn func()) { ch.mu.Lock(); ch.onClose = fn; ch.mu.Unlock() }

func (ch *Channel) deliver(f Frame) {
	ch.mu.Lock()
	od, oj := ch.onData, ch.onJSON
	ch.mu.Unlock()
	switch f.Kind {
	case KindData:
		if od != nil {
			od(f.Payload)
		}
	case KindJSON:
		if oj != nil {
			oj(json.RawMessage(f.Payload))
		}
	}
}

// SendJSON marshals v and sends it as a json frame (credit-controlled).
func (ch *Channel) SendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ch.send(KindJSON, b)
}

// SendBytes sends a binary data frame, blocking while the window is full.
func (ch *Channel) SendBytes(b []byte) error { return ch.send(KindData, b) }

func (ch *Channel) targets() []*conn {
	if ch.owner != nil {
		return []*conn{ch.owner}
	}
	ch.bus.mu.Lock()
	defer ch.bus.mu.Unlock()
	return ch.bus.connList()
}

func (ch *Channel) send(kind byte, payload []byte) error {
	if ch.closed.Load() {
		return ErrClosed
	}
	var firstErr error
	for _, c := range ch.targets() {
		cr := c.credit(ch.id)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-ch.closedCh:
				cancel()
			case <-ctx.Done():
			}
		}()
		err := cr.Acquire(ctx, len(payload), Window)
		cancel()
		if err != nil {
			if ch.closed.Load() {
				return ErrClosed
			}
			if firstErr == nil && !errors.Is(err, ErrClosed) {
				firstErr = err
			}
			continue // connection gone; skip
		}
		if err := c.send(Frame{Kind: kind, Chan: ch.id, Payload: payload}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close closes the channel and tells clients.
func (ch *Channel) Close() {
	if !ch.markClosed(true) {
		return
	}
}

func (ch *Channel) markClosed(notify bool) bool {
	if !ch.closed.CompareAndSwap(false, true) {
		return false
	}
	close(ch.closedCh)
	if notify {
		for _, c := range ch.targets() {
			c.send(Frame{Kind: KindClose, Chan: ch.id})
		}
	}
	if ch.owner != nil {
		ch.owner.mu.Lock()
		delete(ch.owner.chans, ch.id)
		delete(ch.owner.credits, ch.id)
		ch.owner.mu.Unlock()
	} else {
		ch.bus.mu.Lock()
		delete(ch.bus.chans, ch.id)
		ch.bus.mu.Unlock()
	}
	ch.mu.Lock()
	cb := ch.onClose
	ch.mu.Unlock()
	if cb != nil {
		cb()
	}
	return true
}
