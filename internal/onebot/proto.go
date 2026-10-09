// Package onebot contains OneBot V11 wire types and helpers used by the relay.
//
// All cross-boundary payloads are kept as json.RawMessage so that integer
// precision, unknown fields, field order, and the string|array message form are
// preserved byte-for-byte.
package onebot

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Post types.
const (
	PostMessage = "message"
	PostNotice  = "notice"
	PostRequest = "request"
	PostMeta    = "meta_event"
)

// Client roles advertised via X-Client-Role.
const (
	RoleAPI       = "api"
	RoleEvent     = "event"
	RoleUniversal = "universal"
)

// Ret codes.
const (
	RetOK        = 0
	RetBadReq    = 1400
	RetNotFound  = 1404
	RetForbidden = 1403
	RetInternal  = 1500
)

// EventEnvelope extracts routing-relevant fields from an event frame while
// leaving the original bytes untouched.
type EventEnvelope struct {
	PostType      string          `json:"post_type"`
	MessageType   string          `json:"message_type"`
	NoticeType    string          `json:"notice_type"`
	RequestType   string          `json:"request_type"`
	MetaEventType string          `json:"meta_event_type"`
	SubType       string          `json:"sub_type"`
	SelfID        json.RawMessage `json:"self_id"`
	UserID        json.RawMessage `json:"user_id"`
	GroupID       json.RawMessage `json:"group_id"`
}

// ParseEvent decodes only the routing fields of an event frame.
func ParseEvent(raw []byte) (EventEnvelope, bool) {
	var e EventEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, false
	}
	if e.PostType == "" && e.MetaEventType == "" {
		return e, false
	}
	return e, true
}

// ActionFrame is a downstream API request.
type ActionFrame struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
	Echo   json.RawMessage `json:"echo,omitempty"`
	SelfID json.RawMessage `json:"self_id,omitempty"` // relay extension: target account
}

// ParamsSelfID returns params.self_id when present (loose compatibility).
func (a ActionFrame) ParamsSelfID() string {
	if len(a.Params) == 0 {
		return ""
	}
	var p struct {
		SelfID json.RawMessage `json:"self_id"`
	}
	if err := json.Unmarshal(a.Params, &p); err != nil {
		return ""
	}
	return RawID(p.SelfID)
}

// TargetSelfID returns the top-level relay extension self_id, if any.
func (a ActionFrame) TargetSelfID() string { return RawID(a.SelfID) }

// ResponseFrame is an upstream API response.
type ResponseFrame struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    json.RawMessage `json:"echo,omitempty"`
}

// RawID renders a JSON value (number or quoted string) as a plain string.
func RawID(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		s, err := strconv.Unquote(string(raw))
		if err != nil {
			return strings.Trim(string(raw), `"`)
		}
		return s
	}
	return string(raw)
}

// IsEvent reports whether raw is an event frame (has post_type/meta_event_type).
func IsEvent(raw []byte) bool {
	_, ok := ParseEvent(raw)
	return ok
}

// ErrorResponse builds a minimal failed action response echoing echo.
func ErrorResponse(echo json.RawMessage, retcode int, message string) []byte {
	resp := struct {
		Status  string          `json:"status"`
		Retcode int             `json:"retcode"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message,omitempty"`
		Echo    json.RawMessage `json:"echo,omitempty"`
	}{Status: "failed", Retcode: retcode, Data: json.RawMessage("null"), Message: message, Echo: echo}
	out, _ := json.Marshal(resp)
	return out
}
