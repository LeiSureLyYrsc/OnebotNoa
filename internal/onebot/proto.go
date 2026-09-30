// Package onebot models the parts of the OneBot V11 wire protocol the relay has
// to understand.
//
// Hard rule: event frames are NEVER decoded and re-encoded. They travel as raw
// bytes so int64 ids keep their precision, unknown fields survive and field
// order is untouched. Only the outermost envelope of action/response frames is
// inspected, and params/data stay json.RawMessage.
package onebot

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ID is a JSON identifier that may arrive as a number or as a string. It is
// kept as text so a large message_id or user_id never loses precision and so a
// client that stringifies ids is still understood.
type ID string

// UnmarshalJSON accepts numbers, strings and null.
func (id *ID) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case trimmed == "" || trimmed == "null":
		*id = ""
		return nil
	case strings.HasPrefix(trimmed, "\""):
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		*id = ID(v)
		return nil
	case strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "["):
		return fmt.Errorf("onebot: %s is not an id", trimmed)
	default:
		*id = ID(trimmed)
		return nil
	}
}

// MarshalJSON writes the id back verbatim (as a JSON number when it looks like
// one, so a round-tripped frame stays valid).
func (id ID) MarshalJSON() ([]byte, error) {
	if id == "" {
		return []byte("null"), nil
	}
	return []byte(id), nil
}

// String returns the textual form.
func (id ID) String() string { return string(id) }

// Role is the X-Client-Role a reverse-WebSocket client declares.
type Role string

// OneBot role values.
const (
	RoleAPI       Role = "API"
	RoleEvent     Role = "Event"
	RoleUniversal Role = "Universal"
)

// ParseRole normalises a role header; an absent/unknown value means Universal.
func ParseRole(raw string) Role {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "api":
		return RoleAPI
	case "event":
		return RoleEvent
	default:
		return RoleUniversal
	}
}

// CanReceiveEvents reports whether the connection carries events.
func (r Role) CanReceiveEvents() bool { return r == RoleEvent || r == RoleUniversal }

// CanSendActions reports whether the connection can carry API calls.
func (r Role) CanSendActions() bool { return r == RoleAPI || r == RoleUniversal }

// Retcodes. 14xx mirror the HTTP status codes the HTTP transport would use.
const (
	RetOK             = 0
	RetMissingParam   = 100
	RetBadParam       = 102
	RetNotImplemented = 104
	RetImplError      = 1200
	RetBadRequest     = 1400
	RetUnauthorized   = 1401
	RetForbidden      = 1403
	RetNotFound       = 1404
)

// EventMeta is the subset of an event the router needs for filtering. Everything
// else stays in the original bytes.
type EventMeta struct {
	PostType   string
	DetailType string // meta_event_type | message_type | notice_type | request_type
	SubType    string
	SelfID     string
	UserID     string
	GroupID    string
	Time       int64
}

// eventEnvelope decodes only the routing-relevant scalars. Ids use json.Number
// so nothing is ever turned into a float64.
type eventEnvelope struct {
	PostType      string      `json:"post_type"`
	MetaEventType string      `json:"meta_event_type"`
	MessageType   string      `json:"message_type"`
	NoticeType    string      `json:"notice_type"`
	RequestType   string      `json:"request_type"`
	SubType       string      `json:"sub_type"`
	SelfID        ID          `json:"self_id"`
	UserID        ID          `json:"user_id"`
	GroupID       ID          `json:"group_id"`
	Time          int64       `json:"time"`
}

// ParseEventMeta extracts routing metadata. ok is false when the frame is not an
// event (e.g. an action response).
func ParseEventMeta(raw []byte) (EventMeta, bool) {
	var env eventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return EventMeta{}, false
	}
	if env.PostType == "" {
		return EventMeta{}, false
	}
	meta := EventMeta{
		PostType: env.PostType,
		SelfID:   env.SelfID.String(),
		UserID:   env.UserID.String(),
		GroupID:  env.GroupID.String(),
		SubType:  env.SubType,
		Time:     env.Time,
	}
	switch env.PostType {
	case "meta_event":
		meta.DetailType = env.MetaEventType
	case "message":
		meta.DetailType = env.MessageType
	case "notice":
		meta.DetailType = env.NoticeType
	case "request":
		meta.DetailType = env.RequestType
	}
	return meta, true
}

// ExtractSelfID pulls self_id out of any frame that carries one. It backs the
// first-frame identity fallback for clients that never send X-Self-ID.
func ExtractSelfID(raw []byte) string {
	var probe struct {
		SelfID ID `json:"self_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.SelfID.String()
}

// ActionFrame is an inbound API call from a downstream Bot. Params stay raw so
// every parameter, including unknown ones, is forwarded byte-for-byte.
type ActionFrame struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
	Echo   json.RawMessage `json:"echo,omitempty"`
	// SelfID is a relay extension: the Bot states which account the call is for.
	SelfID ID `json:"self_id,omitempty"`
}

// ResponseFrame is a reply to an API call.
type ResponseFrame struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Wording string          `json:"wording,omitempty"`
	Echo    json.RawMessage `json:"echo,omitempty"`
}

// FrameType reports the value of the optional "type" field used by streaming
// extensions (SnowLuma emits type=stream frames before the final response).
func FrameType(raw []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.Type
}

// FailureResponse builds a OneBot failure reply. wording carries the
// human-readable reason (go-cqhttp/NapCat convention).
func FailureResponse(echo json.RawMessage, retcode int, wording string) []byte {
	return buildResponse("failed", retcode, wording, echo, json.RawMessage("null"))
}

// SuccessResponse builds a OneBot success reply with raw data.
func SuccessResponse(echo json.RawMessage, data json.RawMessage) []byte {
	return buildResponse("ok", RetOK, "", echo, data)
}

// buildResponse marshals a reply. A malformed echo (or data) must never blank
// out the payload, so the offending field is dropped instead of failing the
// whole frame.
func buildResponse(status string, retcode int, wording string, echo, data json.RawMessage) []byte {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	if len(echo) > 0 && !json.Valid(echo) {
		echo = nil
	}
	if !json.Valid(data) {
		data = json.RawMessage("null")
	}
	out, err := json.Marshal(ResponseFrame{
		Status:  status,
		Retcode: retcode,
		Data:    data,
		Wording: wording,
		Echo:    echo,
	})
	if err != nil {
		// Last resort: a minimal but valid frame.
		fallback, marshalErr := json.Marshal(map[string]any{
			"status":  status,
			"retcode": retcode,
			"data":    json.RawMessage(data),
		})
		if marshalErr != nil {
			return []byte(`{"status":"failed","retcode":1200,"data":null}`)
		}
		return fallback
	}
	return out
}

// BaseAction strips the _async and _rate_limited suffixes so policy and rate
// limits apply to the underlying action.
func BaseAction(action string) string {
	action = strings.TrimSuffix(action, "_async")
	action = strings.TrimSuffix(action, "_rate_limited")
	return action
}

// IsActionResult reports whether a frame looks like an API reply (it has a
// status field), as opposed to an event.
func IsActionResult(raw []byte) bool {
	var probe struct {
		Status *string `json:"status"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Status != nil
}
