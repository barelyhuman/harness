package host

import (
	"encoding/json"

	"github.com/sid/harness/internal/agent"
)

// Wire message types (NDJSON, one object per line).
const (
	// client -> host
	tHello      = "hello"
	tSend       = "send"
	tPermission = "permission"
	tInterrupt  = "interrupt"
	tSetModel   = "set_model"
	tSetMode    = "set_mode"
	tShutdown   = "shutdown"
	// host -> client
	tEvent = "event"
	tExit  = "exit"
	tError = "error"
)

// msg is the single envelope for both directions.
type msg struct {
	T        string             `json:"t"`
	FromSeq  uint64             `json:"fromSeq,omitempty"`
	Msg      *agent.UserMessage `json:"message,omitempty"`
	ReqID    string             `json:"reqId,omitempty"`
	Decision *agent.Decision    `json:"decision,omitempty"`
	Model    string             `json:"model,omitempty"`
	Mode     string             `json:"mode,omitempty"`
	Event    *agent.Event       `json:"event,omitempty"`
	Code     *int               `json:"code,omitempty"`
	Error    string             `json:"msg,omitempty"`
}

func encode(m msg) []byte {
	b, _ := json.Marshal(m)
	return append(b, '\n')
}
