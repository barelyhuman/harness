package host

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/claude"
)

// DefaultIdleTTL is how long the child is kept alive after a finished turn.
const DefaultIdleTTL = 10 * time.Minute

// maxSockPath is a conservative bound under the macOS sun_path limit (104).
const maxSockPath = 100

// Spec is the JSON handed to the host process via --spec.
type Spec struct {
	RunID     string          `json:"runID"`
	SessionID string          `json:"sessionID"`
	Provider  string          `json:"provider"` // "claude" | "cursor"
	StartOpts agent.StartOpts `json:"startOpts"`
	DataDir   string          `json:"dataDir"`
	// IdleTTLMs overrides DefaultIdleTTL (milliseconds, 0 = default).
	IdleTTLMs int `json:"idleTTLMs,omitempty"`
}

func (s Spec) idleTTL() time.Duration {
	if s.IdleTTLMs > 0 {
		return time.Duration(s.IdleTTLMs) * time.Millisecond
	}
	return DefaultIdleTTL
}

var (
	regMu     sync.Mutex
	providers = map[string]func() agent.Provider{
		claude.ProviderID: func() agent.Provider { return &claude.Provider{} },
	}
)

// RegisterProvider registers (or replaces) a provider factory. The Claude
// provider is pre-registered; the app registers Cursor when it exists.
func RegisterProvider(id string, f func() agent.Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	providers[id] = f
}

func lookupProvider(id string) (agent.Provider, error) {
	regMu.Lock()
	f := providers[id]
	regMu.Unlock()
	if f == nil {
		return nil, fmt.Errorf("host: unknown provider %q", id)
	}
	return f(), nil
}

// Paths.
func runsDir(dataDir string) string { return filepath.Join(dataDir, "runs") }
func logPath(dataDir, id string) string {
	return filepath.Join(runsDir(dataDir), id+".jsonl")
}
func rawPath(dataDir, id string) string {
	return filepath.Join(runsDir(dataDir), id+".raw.jsonl")
}
func infoPath(dataDir, id string) string {
	return filepath.Join(runsDir(dataDir), id+".host.json")
}
func specPath(dataDir, id string) string {
	return filepath.Join(runsDir(dataDir), id+".spec.json")
}
func hostLogPath(dataDir, id string) string {
	return filepath.Join(runsDir(dataDir), id+".host.log")
}

// SocketPath returns the socket path for a run, falling back to a short path
// under /tmp when the preferred one would exceed the sun_path limit.
func SocketPath(dataDir, runID string) string {
	p := filepath.Join(dataDir, "sock", runID+".sock")
	if len(p) < maxSockPath {
		return p
	}
	sum := sha256.Sum256([]byte(p))
	return filepath.Join("/tmp", fmt.Sprintf("harness-%d", os.Getuid()), hex.EncodeToString(sum[:6])+".sock")
}
