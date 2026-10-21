package stream

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

// Tap logs every frame as NDJSON when HARNESS_BUS_TAP=1.
type Tap struct {
	mu sync.Mutex
	f  *os.File
}

const tapMaxPayload = 1024

// NewTapFromEnv returns a Tap writing to
// ~/Library/Application Support/Harness/logs/bus-<ts>.ndjson, or a no-op Tap.
func NewTapFromEnv() *Tap {
	if os.Getenv("HARNESS_BUS_TAP") != "1" {
		return &Tap{}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return &Tap{}
	}
	dir := filepath.Join(home, "Library", "Application Support", "Harness", "logs")
	return NewTap(dir)
}

// NewTap opens bus-<ts>.ndjson in dir.
func NewTap(dir string) *Tap {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &Tap{}
	}
	f, err := os.Create(filepath.Join(dir, "bus-"+time.Now().Format("20060102-150405")+".ndjson"))
	if err != nil {
		return &Tap{}
	}
	return &Tap{f: f}
}

// Path returns the log file path ("" if disabled).
func (t *Tap) Path() string {
	if t == nil || t.f == nil {
		return ""
	}
	return t.f.Name()
}

type tapRec struct {
	T       int64  `json:"t"`
	Dir     string `json:"dir"`
	Kind    byte   `json:"kind"`
	Chan    uint32 `json:"chan"`
	Len     int    `json:"len"`
	Payload string `json:"payload,omitempty"`
	B64     string `json:"b64,omitempty"`
}

// Log records a frame; dir is "in" (client->Go) or "out".
func (t *Tap) Log(dir string, f Frame) {
	if t == nil || t.f == nil {
		return
	}
	r := tapRec{T: time.Now().UnixMilli(), Dir: dir, Kind: f.Kind, Chan: f.Chan, Len: len(f.Payload)}
	p := f.Payload
	if len(p) > tapMaxPayload {
		p = p[:tapMaxPayload]
	}
	if f.Kind == KindAck && len(f.Payload) == 4 {
		n, _ := ParseAck(f.Payload)
		r.Payload = json.Number(itoa(int(n))).String()
	} else if utf8.Valid(p) {
		r.Payload = string(p)
	} else {
		r.B64 = base64.StdEncoding.EncodeToString(p)
	}
	b, _ := json.Marshal(r)
	t.mu.Lock()
	t.f.Write(append(b, '\n'))
	t.mu.Unlock()
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (t *Tap) Close() {
	if t == nil || t.f == nil {
		return
	}
	t.mu.Lock()
	t.f.Close()
	t.f = nil
	t.mu.Unlock()
}
