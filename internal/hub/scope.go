package hub

import (
	"encoding/json"
	"fmt"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Scope is the per-binding filter that decides which events a Bot receives.
type Scope struct {
	PostTypes     []string `json:"post_types"`
	IncludeGroups []string `json:"include_groups"`
	ExcludeGroups []string `json:"exclude_groups"`
	IncludeUsers  []string `json:"include_users"`
	ExcludeUsers  []string `json:"exclude_users"`
	ExcludeSelf   bool     `json:"exclude_self"`
	// MetaEvents is synthetic | passthrough | drop; empty inherits the global
	// policy.
	MetaEvents string `json:"meta_events"`
}

// ParseScope decodes a scope, tolerating an empty document.
func ParseScope(raw json.RawMessage) (Scope, error) {
	var s Scope
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Scope{}, fmt.Errorf("hub: invalid binding scope: %w", err)
	}
	return s, nil
}

// MetaEventMode resolves the effective meta-event policy for this binding.
func (s Scope) MetaEventMode(fallback string) string {
	switch s.MetaEvents {
	case "synthetic", "passthrough", "drop":
		return s.MetaEvents
	default:
		if fallback == "" {
			return "synthetic"
		}
		return fallback
	}
}

// Match reports whether an event passes the filter. An empty scope matches
// everything except meta events in synthetic mode (which are never forwarded
// from upstream).
func (s Scope) Match(meta onebot.EventMeta, globalMetaEvents string) bool {
	if len(s.PostTypes) > 0 && !containsString(s.PostTypes, meta.PostType) {
		return false
	}
	if meta.PostType == "meta_event" {
		return s.MetaEventMode(globalMetaEvents) == "passthrough"
	}
	if len(s.IncludeGroups) > 0 && !containsString(s.IncludeGroups, meta.GroupID) {
		return false
	}
	if containsString(s.ExcludeGroups, meta.GroupID) && meta.GroupID != "" {
		return false
	}
	if len(s.IncludeUsers) > 0 && !containsString(s.IncludeUsers, meta.UserID) {
		return false
	}
	if containsString(s.ExcludeUsers, meta.UserID) && meta.UserID != "" {
		return false
	}
	if s.ExcludeSelf && meta.UserID != "" && meta.UserID == meta.SelfID {
		return false
	}
	return true
}

func containsString(list []string, want string) bool {
	if want == "" {
		return false
	}
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
