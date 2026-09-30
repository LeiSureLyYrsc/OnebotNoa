package hub

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Metrics collects process counters. It implements Observer and TrafficObserver,
// so wiring it costs nothing on the hot path beyond a few atomic increments.
type Metrics struct {
	started time.Time

	upstreamFrames     atomic.Uint64
	actionsForwarded   atomic.Uint64
	responsesForwarded atomic.Uint64
	actionsRejected    atomic.Uint64
	actionsTimedOut    atomic.Uint64
	accountEvents      atomic.Uint64
}

// NewMetrics builds the collector.
func NewMetrics() *Metrics { return &Metrics{started: time.Now()} }

// AccountChanged implements Observer.
func (m *Metrics) AccountChanged(AccountEvent) { m.accountEvents.Add(1) }

// UpstreamFrame implements Observer.
func (m *Metrics) UpstreamFrame(string, onebot.Role, []byte) { m.upstreamFrames.Add(1) }

// BotAction implements TrafficObserver.
func (m *Metrics) BotAction(string, string, []byte) { m.actionsForwarded.Add(1) }

// BotResponse implements TrafficObserver.
func (m *Metrics) BotResponse(string, []byte) { m.responsesForwarded.Add(1) }

// PolicyRejected implements TrafficObserver.
func (m *Metrics) PolicyRejected(string, string, int, string) { m.actionsRejected.Add(1) }

// ActionTimedOut implements TrafficObserver.
func (m *Metrics) ActionTimedOut(string, string) { m.actionsTimedOut.Add(1) }

// Snapshot is the JSON view used by the management API and the dashboard.
type Snapshot struct {
	UptimeSeconds      int64  `json:"uptime_seconds"`
	UpstreamFrames     uint64 `json:"upstream_frames"`
	ActionsForwarded   uint64 `json:"actions_forwarded"`
	ResponsesForwarded uint64 `json:"responses_forwarded"`
	ActionsRejected    uint64 `json:"actions_rejected"`
	ActionsTimedOut    uint64 `json:"actions_timed_out"`
	AccountEvents      uint64 `json:"account_events"`
	RateLimitedAccount uint64 `json:"rate_limited_account"`
	RateLimitedBot     uint64 `json:"rate_limited_bot"`
	RateLimitedInflight uint64 `json:"rate_limited_inflight"`
	OfflineQueued      int    `json:"offline_queued"`
}

// Snapshot reads the counters (optionally enriched with limiter state).
func (m *Metrics) Snapshot(limiter *Limiter, policy *PolicyEngine) Snapshot {
	snap := Snapshot{
		UptimeSeconds:      int64(time.Since(m.started).Seconds()),
		UpstreamFrames:     m.upstreamFrames.Load(),
		ActionsForwarded:   m.actionsForwarded.Load(),
		ResponsesForwarded: m.responsesForwarded.Load(),
		ActionsRejected:    m.actionsRejected.Load(),
		ActionsTimedOut:    m.actionsTimedOut.Load(),
		AccountEvents:      m.accountEvents.Load(),
	}
	if limiter != nil {
		snap.RateLimitedAccount, snap.RateLimitedBot, snap.RateLimitedInflight = limiter.Counters()
	}
	if policy != nil {
		snap.OfflineQueued = policy.OfflineLen()
	}
	return snap
}

// Write renders the Prometheus text exposition format.
func (m *Metrics) Write(w io.Writer, status Status, events *EventLog, limiter *Limiter, policy *PolicyEngine) error {
	lines := []string{
		"# HELP onebotnoa_up Whether the relay process is running.",
		"# TYPE onebotnoa_up gauge",
		"onebotnoa_up 1",
		fmt.Sprintf("# HELP onebotnoa_uptime_seconds Process uptime.\n# TYPE onebotnoa_uptime_seconds counter\nonebotnoa_uptime_seconds %d", int64(time.Since(m.started).Seconds())),

		"# HELP onebotnoa_accounts QQ instances by live state.\n# TYPE onebotnoa_accounts gauge",
		fmt.Sprintf("onebotnoa_accounts{state=\"online\"} %d", status.Online),
		fmt.Sprintf("onebotnoa_accounts{state=\"degraded\"} %d", status.Degraded),
		fmt.Sprintf("onebotnoa_accounts{state=\"offline\"} %d", status.Offline),
		fmt.Sprintf("# HELP onebotnoa_upstream_connections QQ-side physical connections.\n# TYPE onebotnoa_upstream_connections gauge\nonebotnoa_upstream_connections %d", status.UpstreamConns),
		fmt.Sprintf("# HELP onebotnoa_downstream_connections Bot-side physical connections.\n# TYPE onebotnoa_downstream_connections gauge\nonebotnoa_downstream_connections %d", status.DownstreamConns),
		fmt.Sprintf("# HELP onebotnoa_pending_accounts Connections waiting for approval.\n# TYPE onebotnoa_pending_accounts gauge\nonebotnoa_pending_accounts %d", status.PendingAccounts),
		fmt.Sprintf("# HELP onebotnoa_pending_actions Actions awaiting an upstream reply.\n# TYPE onebotnoa_pending_actions gauge\nonebotnoa_pending_actions %d", status.PendingActions),

		"# HELP onebotnoa_frames_total Frames received from QQ-side implementations.\n# TYPE onebotnoa_frames_total counter",
		fmt.Sprintf("onebotnoa_frames_total{direction=\"upstream\"} %d", m.upstreamFrames.Load()),

		"# HELP onebotnoa_actions_total Actions handled by the relay.\n# TYPE onebotnoa_actions_total counter",
		fmt.Sprintf("onebotnoa_actions_total{result=\"forwarded\"} %d", m.actionsForwarded.Load()),
		fmt.Sprintf("onebotnoa_actions_total{result=\"rejected\"} %d", m.actionsRejected.Load()),
		fmt.Sprintf("onebotnoa_actions_total{result=\"timeout\"} %d", m.actionsTimedOut.Load()),
		fmt.Sprintf("# HELP onebotnoa_responses_total Replies returned to Bots.\n# TYPE onebotnoa_responses_total counter\nonebotnoa_responses_total %d", m.responsesForwarded.Load()),
		fmt.Sprintf("# HELP onebotnoa_account_events_total Connection/state transitions.\n# TYPE onebotnoa_account_events_total counter\nonebotnoa_account_events_total %d", m.accountEvents.Load()),
	}

	if limiter != nil {
		account, bot, inflight := limiter.Counters()
		lines = append(lines,
			"# HELP onebotnoa_rate_limited_total Actions refused by a protection.\n# TYPE onebotnoa_rate_limited_total counter",
			fmt.Sprintf("onebotnoa_rate_limited_total{scope=\"account\"} %d", account),
			fmt.Sprintf("onebotnoa_rate_limited_total{scope=\"bot\"} %d", bot),
			fmt.Sprintf("onebotnoa_rate_limited_total{scope=\"inflight\"} %d", inflight),
			fmt.Sprintf("# HELP onebotnoa_rate_limit_buckets Tracked token buckets.\n# TYPE onebotnoa_rate_limit_buckets gauge\nonebotnoa_rate_limit_buckets %d", limiter.Buckets()),
		)
	}
	if policy != nil {
		lines = append(lines,
			fmt.Sprintf("# HELP onebotnoa_offline_queue Actions buffered for offline accounts.\n# TYPE onebotnoa_offline_queue gauge\nonebotnoa_offline_queue %d", policy.OfflineLen()),
		)
	}
	if events != nil {
		lines = append(lines,
			fmt.Sprintf("# HELP onebotnoa_event_ring_size Records currently in the live-view ring.\n# TYPE onebotnoa_event_ring_size gauge\nonebotnoa_event_ring_size %d", events.Len()),
			fmt.Sprintf("# HELP onebotnoa_event_subscribers Live SSE consumers.\n# TYPE onebotnoa_event_subscribers gauge\nonebotnoa_event_subscribers %d", events.Subscribers()),
			fmt.Sprintf("# HELP onebotnoa_event_dropped_total Records dropped for slow SSE consumers.\n# TYPE onebotnoa_event_dropped_total counter\nonebotnoa_event_dropped_total %d", events.Dropped()),
		)
	}

	for _, line := range lines {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return err
		}
	}
	return nil
}
