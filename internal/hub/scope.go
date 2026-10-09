package hub

import (
	"encoding/json"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Scope filters which events a binding forwards to its bot.
type Scope struct {
	PostTypes     []string `json:"post_types"`
	IncludeGroups []string `json:"include_groups"`
	ExcludeGroups []string `json:"exclude_groups"`
	IncludeUsers  []string `json:"include_users"`
	ExcludeUsers  []string `json:"exclude_users"`
	ExcludeSelf   bool     `json:"exclude_self"`
}

func parseScope(raw string) Scope {
	var s Scope
	if strings.TrimSpace(raw) == "" {
		return s
	}
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

// scopeMatch reports whether env passes the binding scope.
func scopeMatch(rawScope string, env onebot.EventEnvelope, selfID string) bool {
	s := parseScope(rawScope)
	if len(s.PostTypes) > 0 {
		ok := false
		for _, p := range s.PostTypes {
			if p == env.PostType {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	user := onebot.RawID(env.UserID)
	group := onebot.RawID(env.GroupID)

	if s.ExcludeSelf && user != "" && user == selfID {
		return false
	}
	if user != "" {
		if contains(s.ExcludeUsers, user) {
			return false
		}
		if len(s.IncludeUsers) > 0 && !contains(s.IncludeUsers, user) {
			return false
		}
	}
	if group != "" {
		if contains(s.ExcludeGroups, group) {
			return false
		}
		if len(s.IncludeGroups) > 0 && !contains(s.IncludeGroups, group) {
			return false
		}
	}
	return true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
