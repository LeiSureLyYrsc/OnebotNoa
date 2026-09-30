package hub

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// PolicyEngine decides whether a Bot may run an action right now, and buffers
// actions for accounts that are temporarily offline when the operator asked for
// that behaviour.
type PolicyEngine struct {
	cfg     *config.Config
	logger  *slog.Logger
	limiter *Limiter

	mu      sync.Mutex
	offline map[string][]queuedAction
	queued  int
}

type queuedAction struct {
	frame   []byte
	expires time.Time
}

// PreSendRequest carries everything the decision needs.
type PreSendRequest struct {
	Bot    model.Bot
	SelfID string
	// Action is the base action name (the _async/_rate_limited suffixes are
	// already stripped) so policy matches what the operator wrote down.
	Action string
	Scope  Scope
}

// NewPolicyEngine builds the engine from configuration.
func NewPolicyEngine(cfg *config.Config, logger *slog.Logger) *PolicyEngine {
	if logger == nil {
		logger = slog.Default()
	}
	return &PolicyEngine{
		cfg:     cfg,
		logger:  logger,
		limiter: NewLimiter(cfg.Policy),
		offline: map[string][]queuedAction{},
	}
}

// Limiter exposes the token buckets (metrics, tests).
func (e *PolicyEngine) Limiter() *Limiter { return e.limiter }

// actionPolicy is the Bot-level allow/deny list.
type actionPolicy struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

func parseActionPolicy(raw json.RawMessage) actionPolicy {
	var policy actionPolicy
	if len(raw) == 0 {
		return policy
	}
	_ = json.Unmarshal(raw, &policy)
	return policy
}

// matchesAction supports exact names and a trailing "*" wildcard.
func matchesAction(patterns []string, action string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(action, strings.TrimSuffix(pattern, "*")) {
				return true
			}
			continue
		}
		if strings.EqualFold(pattern, action) {
			return true
		}
	}
	return false
}

// Allow implements PreSendHook. When it allows, it has already reserved an
// in-flight slot which the caller must release through Release (the pending
// table does that exactly once).
func (e *PolicyEngine) Allow(req PreSendRequest) (int, string, bool) {
	policy := parseActionPolicy(req.Bot.ActionPolicy)
	if matchesAction(policy.Deny, req.Action) {
		return onebot.RetForbidden, fmt.Sprintf("动作 %s 被该 Bot 的黑名单禁止", req.Action), false
	}
	if len(policy.Allow) > 0 && !matchesAction(policy.Allow, req.Action) {
		return onebot.RetForbidden, fmt.Sprintf("动作 %s 不在该 Bot 的白名单内", req.Action), false
	}

	// Concurrency applies to every action, read-only ones included.
	if !e.limiter.Acquire(req.Bot.Name) {
		return onebot.RetImplError, fmt.Sprintf("Bot %s 的在途动作已达上限", req.Bot.Name), false
	}

	if ReadOnlyAction(req.Action) {
		return 0, "", true
	}
	if allowed, wait := e.limiter.AllowBot(req.Bot.Name, req.SelfID); !allowed {
		e.limiter.Release(req.Bot.Name)
		return onebot.RetImplError, fmt.Sprintf("该 Bot 对账号 %s 的配额已满，约 %s 后重试", req.SelfID, humanWait(wait)), false
	}
	if allowed, wait := e.limiter.AllowAccount(req.SelfID); !allowed {
		e.limiter.Release(req.Bot.Name)
		return onebot.RetImplError, fmt.Sprintf("账号 %s 触发全局限速（多个 Bot 共享该配额），约 %s 后重试", req.SelfID, humanWait(wait)), false
	}
	return 0, "", true
}

// Release returns an in-flight slot.
func (e *PolicyEngine) Release(botName string) { e.limiter.Release(botName) }

// QueueOffline stores a frame for an offline account. It reports false when the
// offline policy is fail_fast, or when the queue is full.
func (e *PolicyEngine) QueueOffline(selfID string, frame []byte) bool {
	if e.cfg.Policy.OfflineActionPolicy != "queue" {
		return false
	}
	limit := e.cfg.Policy.Queue.Size
	if limit <= 0 {
		return false
	}
	ttl := e.cfg.Policy.Queue.TTL.Std()
	if ttl <= 0 {
		ttl = 10 * time.Second
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	nowT := time.Now()
	e.dropExpiredLocked(nowT)
	if e.queued >= limit {
		return false
	}
	e.offline[selfID] = append(e.offline[selfID], queuedAction{frame: frame, expires: nowT.Add(ttl)})
	e.queued++
	return true
}

// FlushOffline delivers queued frames in order. send reports whether a frame was
// accepted; the first refusal stops the flush and keeps the rest queued.
func (e *PolicyEngine) FlushOffline(selfID string, send func([]byte) bool) int {
	e.mu.Lock()
	pending := e.offline[selfID]
	delete(e.offline, selfID)
	e.queued -= len(pending)
	e.mu.Unlock()

	if len(pending) == 0 {
		return 0
	}
	nowT := time.Now()
	delivered := 0
	for i, item := range pending {
		if nowT.After(item.expires) {
			e.logger.Debug("dropping an expired queued action", "self_id", selfID)
			continue
		}
		if !send(item.frame) {
			rest := pending[i:]
			e.mu.Lock()
			e.offline[selfID] = append(e.offline[selfID], rest...)
			e.queued += len(rest)
			e.mu.Unlock()
			break
		}
		delivered++
	}
	if delivered > 0 {
		e.logger.Info("delivered queued actions after an account came online",
			"self_id", selfID, "count", delivered)
	}
	return delivered
}

// OfflineLen reports how many frames are waiting for offline accounts.
func (e *PolicyEngine) OfflineLen() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.queued
}

// SweepOffline forgets expired queued frames.
func (e *PolicyEngine) SweepOffline() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	before := e.queued
	e.dropExpiredLocked(time.Now())
	return before - e.queued
}

func (e *PolicyEngine) dropExpiredLocked(nowT time.Time) {
	for selfID, items := range e.offline {
		kept := items[:0:0]
		for _, item := range items {
			if nowT.After(item.expires) {
				e.queued--
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(e.offline, selfID)
			continue
		}
		e.offline[selfID] = kept
	}
	if e.queued < 0 {
		e.queued = 0
	}
}

// humanWait renders a short retry hint.
func humanWait(wait time.Duration) string {
	if wait <= 0 {
		return "片刻"
	}
	if wait < time.Second {
		return fmt.Sprintf("%dms", wait.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", wait.Seconds())
}
