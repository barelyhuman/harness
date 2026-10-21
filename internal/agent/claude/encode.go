package claude

import (
	"encoding/base64"
	"encoding/json"

	"github.com/sid/harness/internal/agent"
)

// All Encode* functions return one complete stdin line including the
// trailing newline.

func line(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Encoders only marshal plain maps/structs of strings; unreachable.
		panic("claude: encode: " + err.Error())
	}
	return string(b) + "\n"
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type imageBlock struct {
	Type   string      `json:"type"`
	Source imageSource `json:"source"`
}

// EncodeUser encodes a user turn. Text-only turns use a plain string content
// (as in the recorded fixtures); with images the content is a block array
// (images first).
func EncodeUser(m agent.UserMessage) string {
	var content any = m.Text
	if len(m.Images) > 0 {
		blocks := make([]any, 0, len(m.Images)+1)
		for _, im := range m.Images {
			blocks = append(blocks, imageBlock{Type: "image", Source: imageSource{
				Type: "base64", MediaType: im.MediaType, Data: base64.StdEncoding.EncodeToString(im.Data),
			}})
		}
		if m.Text != "" {
			blocks = append(blocks, textBlock{Type: "text", Text: m.Text})
		}
		content = blocks
	}
	return line(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	})
}

// EncodeControlRequest encodes a control_request. extra fields are merged into
// the inner request next to subtype.
func EncodeControlRequest(id, subtype string, extra map[string]any) string {
	req := map[string]any{"subtype": subtype}
	for k, v := range extra {
		req[k] = v
	}
	return line(map[string]any{"type": "control_request", "request_id": id, "request": req})
}

// DefaultDenyMessage is sent when a deny carries no message.
const DefaultDenyMessage = "User denied this action"

// EncodePermission encodes the nested control_response answering a
// can_use_tool request. tool and origInput come from the pending request:
// origInput is echoed as updatedInput when d.UpdatedInput is empty (the SDK
// schema requires it); tool names the rule for allow_always.
func EncodePermission(reqID string, d agent.Decision, tool string, origInput json.RawMessage) string {
	body := map[string]any{}
	switch d.Behavior {
	case agent.BehaviorDeny:
		msg := d.Message
		if msg == "" {
			msg = DefaultDenyMessage
		}
		body["behavior"] = "deny"
		body["message"] = msg
	default: // allow, allow_always
		body["behavior"] = "allow"
		in := d.UpdatedInput
		if len(in) == 0 {
			in = origInput
		}
		if len(in) == 0 {
			in = json.RawMessage(`{}`)
		}
		body["updatedInput"] = in
		if d.Behavior == agent.BehaviorAllowAlways {
			body["updatedPermissions"] = []any{map[string]any{
				"type":        "addRules",
				"rules":       []any{map[string]any{"toolName": tool}},
				"behavior":    "allow",
				"destination": "session",
			}}
		}
	}
	return line(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
			"response":   body,
		},
	})
}

// EncodeControlError answers a CLI request we do not support.
func EncodeControlError(reqID, msg string) string {
	return line(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": reqID,
			"error":      msg,
		},
	})
}
