package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sid/harness/internal/agent"
)

// ErrHostGone means the host process is dead or its socket is missing.
var ErrHostGone = errors.New("host: gone")

// Client is the app-side connection to one run host.
type Client struct {
	RunID string

	c      net.Conn
	wmu    sync.Mutex
	events chan agent.Event

	mu       sync.Mutex
	exitCode *int
	lastErr  string
	done     chan struct{}
}

func newClient(c net.Conn, runID string, fromSeq uint64) (*Client, error) {
	cl := &Client{RunID: runID, c: c, events: make(chan agent.Event, 1024), done: make(chan struct{})}
	if err := cl.write(msg{T: tHello, FromSeq: fromSeq}); err != nil {
		c.Close()
		return nil, err
	}
	go cl.readLoop()
	return cl, nil
}

func (c *Client) write(m msg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.c.Write(encode(m))
	return err
}

func (c *Client) readLoop() {
	defer close(c.done)
	defer close(c.events)
	r := bufio.NewReaderSize(c.c, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m msg
			if json.Unmarshal(line, &m) == nil {
				switch m.T {
				case tEvent:
					if m.Event != nil {
						c.events <- *m.Event
					}
				case tExit:
					c.mu.Lock()
					c.exitCode = m.Code
					c.mu.Unlock()
				case tError:
					c.mu.Lock()
					c.lastErr = m.Error
					c.mu.Unlock()
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// Events streams replayed then live events; closed when the connection ends.
func (c *Client) Events() <-chan agent.Event { return c.events }

// Done is closed when the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// ExitCode reports the child's exit code once the host announced `exit`.
func (c *Client) ExitCode() (code int, exited bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exitCode == nil {
		return 0, false
	}
	return *c.exitCode, true
}

// LastError is the most recent `error` message from the host.
func (c *Client) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

func (c *Client) Send(m agent.UserMessage) error { return c.write(msg{T: tSend, Msg: &m}) }
func (c *Client) Permission(reqID string, d agent.Decision) error {
	return c.write(msg{T: tPermission, ReqID: reqID, Decision: &d})
}
func (c *Client) Interrupt() error { return c.write(msg{T: tInterrupt}) }
func (c *Client) SetModel(model string) error {
	return c.write(msg{T: tSetModel, Model: model})
}
func (c *Client) SetMode(mode string) error { return c.write(msg{T: tSetMode, Mode: mode}) }

// Shutdown asks the host to stop the run (interrupt, close stdin, exit).
func (c *Client) Shutdown() error { return c.write(msg{T: tShutdown}) }

// Close drops the connection; the run is unaffected.
func (c *Client) Close() error { return c.c.Close() }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readInfo(dataDir, runID string) (HostInfo, error) {
	b, err := os.ReadFile(infoPath(dataDir, runID))
	if err != nil {
		return HostInfo{}, err
	}
	var info HostInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return HostInfo{}, err
	}
	return info, nil
}

// Attach connects to a running host and replays from fromSeq (0 = start).
func Attach(dataDir, runID string, fromSeq uint64) (*Client, error) {
	info, err := readInfo(dataDir, runID)
	if err != nil || !pidAlive(info.PID) || info.ExitCode != nil {
		return nil, ErrHostGone
	}
	if _, err := os.Stat(info.Socket); err != nil {
		return nil, ErrHostGone
	}
	nc, err := net.DialTimeout("unix", info.Socket, 2*time.Second)
	if err != nil {
		return nil, ErrHostGone
	}
	return newClient(nc, runID, fromSeq)
}

// Scan lists the host.json files under dataDir and whether each host is alive.
func Scan(dataDir string) []HostInfo {
	files, _ := filepath.Glob(filepath.Join(runsDir(dataDir), "*.host.json"))
	var out []HostInfo
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var info HostInfo
		if json.Unmarshal(b, &info) != nil {
			continue
		}
		info.Alive = info.ExitCode == nil && pidAlive(info.PID)
		out = append(out, info)
	}
	return out
}

// ReadLog reads events with Seq >= fromSeq from a run's JSONL log.
func ReadLog(dataDir, runID string, fromSeq uint64) ([]agent.Event, error) {
	return readLogFile(logPath(dataDir, runID), fromSeq)
}

func readLogFile(path string, fromSeq uint64) ([]agent.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []agent.Event
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 && line[len(line)-1] == '\n' {
			var e agent.Event
			if json.Unmarshal(line, &e) == nil && e.Seq >= fromSeq {
				out = append(out, e)
			}
		}
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
	}
}

// Launcher starts detached hosts.
type Launcher struct {
	// Exe overrides os.Executable() (tests).
	Exe string
	// Timeout for the socket to appear (default 5s).
	Timeout time.Duration
}

// Launch starts a detached host for spec and connects to it.
func Launch(spec Spec) (*Client, error) { return (&Launcher{}).Launch(spec) }

// Launch writes the spec, starts the host (new session, stdio to /dev/null,
// stderr to <runID>.host.log) and connects once the socket is up.
func (l *Launcher) Launch(spec Spec) (*Client, error) {
	if spec.RunID == "" || spec.DataDir == "" {
		return nil, errors.New("host: spec needs runID and dataDir")
	}
	if err := os.MkdirAll(runsDir(spec.DataDir), 0o755); err != nil {
		return nil, err
	}
	// A stale host.json from an earlier host must not satisfy the wait below.
	_ = os.Remove(infoPath(spec.DataDir, spec.RunID))
	sock := SocketPath(spec.DataDir, spec.RunID)
	_ = os.Remove(sock)
	sp := specPath(spec.DataDir, spec.RunID)
	b, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(sp, b, 0o644); err != nil {
		return nil, err
	}
	exe := l.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, err
		}
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer devnull.Close()
	lf, err := os.OpenFile(hostLogPath(spec.DataDir, spec.RunID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	cmd := exec.Command(exe, "--agent-host", "--run", spec.RunID, "--spec", sp)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Instead of Process.Release we reap in the background: a released child
	// that exits before the app would linger as a zombie, and kill(pid,0)
	// would report it alive. Setsid keeps the host independent of us.
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	timeout := l.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(sock); err == nil {
			if nc, err := net.DialTimeout("unix", sock, time.Second); err == nil {
				return newClient(nc, spec.RunID, 0)
			}
		}
		select {
		case <-exited:
			return nil, fmt.Errorf("host: exited during startup: %s", logTail(hostLogPath(spec.DataDir, spec.RunID)))
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("host: socket %s did not appear: %s", sock, logTail(hostLogPath(spec.DataDir, spec.RunID)))
		}
	}
}

func logTail(p string) string {
	b, _ := os.ReadFile(p)
	if len(b) > 1000 {
		b = b[len(b)-1000:]
	}
	return strings.TrimSpace(string(b))
}
