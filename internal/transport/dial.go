package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// EndpointSpec is one relay-dialed connection, from config.yaml or the database.
type EndpointSpec struct {
	DBID        int64
	Name        string
	Kind        string
	URL         string
	Mode        string
	Token       string
	AccountHint string
	BotID       int64
	BotName     string
	FixedSelfID string
	Enabled     bool
	Source      string
	Reconnect   config.Reconnect
}

// EndpointState is the runtime view of a dial target for the WebUI.
type EndpointState struct {
	DBID        int64     `json:"db_id,omitempty"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	URL         string    `json:"url"`
	Mode        string    `json:"mode,omitempty"`
	AccountHint string    `json:"account_hint,omitempty"`
	BotID       int64     `json:"bot_id,omitempty"`
	BotName     string    `json:"bot_name,omitempty"`
	FixedSelfID string    `json:"fixed_self_id,omitempty"`
	Enabled     bool      `json:"enabled"`
	Source      string    `json:"source"`
	HasToken    bool      `json:"has_token"`
	State       string    `json:"state"`
	LastError   string    `json:"last_error,omitempty"`
	Attempts    int       `json:"attempts"`
	ConnectedAt time.Time `json:"connected_at,omitempty"`
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`
}

// dialWorker supervises one endpoint: connect, serve, back off, retry.
type dialWorker struct {
	spec   EndpointSpec
	cancel context.CancelFunc

	mu    sync.Mutex
	state EndpointState
	peers map[string]hub.Peer
}

func (w *dialWorker) setState(state string, lastErr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.State = state
	if lastErr != "" {
		w.state.LastError = lastErr
	}
	if state != "backoff" {
		w.state.NextRetryAt = time.Time{}
	}
	if state == "online" {
		w.state.ConnectedAt = time.Now()
		w.state.LastError = ""
	}
}

func (w *dialWorker) recordFailure(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.Attempts++
	if err != nil {
		w.state.LastError = err.Error()
	}
}

func (w *dialWorker) setNextRetry(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.NextRetryAt = at
}

func (w *dialWorker) track(peer hub.Peer) {
	w.mu.Lock()
	w.peers[peer.ID()] = peer
	w.mu.Unlock()
}

func (w *dialWorker) untrack(peer hub.Peer) {
	w.mu.Lock()
	delete(w.peers, peer.ID())
	w.mu.Unlock()
}

// closePeers drops every live connection of this endpoint (forced reconnect or
// shutdown); the worker then dials again.
func (w *dialWorker) closePeers(reason string) {
	w.mu.Lock()
	peers := make([]hub.Peer, 0, len(w.peers))
	for _, peer := range w.peers {
		peers = append(peers, peer)
	}
	w.mu.Unlock()
	for _, peer := range peers {
		peer.Close(websocket.CloseGoingAway, reason)
	}
}

// DialManager owns every outbound connection of the hub.
type DialManager struct {
	cfg    *config.Config
	store  *store.Store
	hub    *hub.Hub
	logger *slog.Logger
	dialer websocket.Dialer

	seq atomic.Uint64

	mu      sync.Mutex
	workers map[string]*dialWorker
	ctx     context.Context
}

// NewDialManager builds the manager.
func NewDialManager(cfg *config.Config, st *store.Store, relay *hub.Hub, logger *slog.Logger) *DialManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &DialManager{
		cfg:     cfg,
		store:   st,
		hub:     relay,
		logger:  logger,
		workers: map[string]*dialWorker{},
		dialer: websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
		},
	}
}

// Start launches a worker per enabled endpoint.
func (m *DialManager) Start(ctx context.Context, specs []EndpointSpec) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	m.Apply(specs)
}

// Apply reconciles the running workers with the desired endpoints: unchanged
// specs keep their connection, changed ones restart, removed ones stop.
func (m *DialManager) Apply(specs []EndpointSpec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return
	}

	desired := map[string]EndpointSpec{}
	for _, spec := range specs {
		desired[spec.Name] = spec
	}

	for name, worker := range m.workers {
		spec, keep := desired[name]
		if keep && worker.spec == spec {
			continue
		}
		m.logger.Info("stopping dial endpoint", "endpoint", name, "replaced", keep)
		if worker.cancel != nil {
			worker.cancel()
		}
		worker.closePeers("endpoint reconfigured")
		delete(m.workers, name)
	}

	for name, spec := range desired {
		if _, running := m.workers[name]; running {
			continue
		}
		if !spec.Enabled {
			worker := &dialWorker{spec: spec, peers: map[string]hub.Peer{}}
			worker.state = EndpointState{
				DBID: spec.DBID, Name: name, Kind: spec.Kind, URL: spec.URL, Mode: spec.Mode,
				AccountHint: spec.AccountHint, BotID: spec.BotID, BotName: spec.BotName,
				FixedSelfID: spec.FixedSelfID,
				Enabled:     false, Source: spec.Source, HasToken: spec.Token != "",
				State: "disabled",
			}
			m.workers[name] = worker
			continue
		}
		workerCtx, cancel := context.WithCancel(m.ctx)
		worker := &dialWorker{spec: spec, cancel: cancel, peers: map[string]hub.Peer{}}
		worker.state = EndpointState{
			DBID: spec.DBID, Name: name, Kind: spec.Kind, URL: spec.URL, Mode: spec.Mode,
			AccountHint: spec.AccountHint, BotID: spec.BotID, BotName: spec.BotName,
			FixedSelfID: spec.FixedSelfID,
			Enabled:     true, Source: spec.Source, HasToken: spec.Token != "",
			State: "idle",
		}
		m.workers[name] = worker
		go m.runWorker(workerCtx, worker)
		m.logger.Info("started dial endpoint",
			"endpoint", name, "kind", spec.Kind, "url", spec.URL, "mode", spec.Mode)
	}
}

// Reload re-reads config.yaml plus the database and applies the result; the API
// calls it after an endpoint changes.
func (m *DialManager) Reload(ctx context.Context) {
	specs := LoadEndpointSpecs(ctx, m.cfg, m.store, m.logger)
	m.Apply(specs)
}

// States returns the runtime view of every endpoint, sorted by name.
func (m *DialManager) States() []EndpointState {
	m.mu.Lock()
	workers := make([]*dialWorker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.mu.Unlock()

	out := make([]EndpointState, 0, len(workers))
	for _, worker := range workers {
		worker.mu.Lock()
		state := worker.state
		worker.mu.Unlock()
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ForceReconnect drops the current connection of one endpoint; the worker then
// retries immediately.
func (m *DialManager) ForceReconnect(name string) bool {
	m.mu.Lock()
	worker, ok := m.workers[name]
	m.mu.Unlock()
	if !ok {
		return false
	}
	m.logger.Info("forcing reconnect", "endpoint", name)
	worker.closePeers("forced reconnect")
	return true
}

// Stop closes every dialed connection and stops the workers.
func (m *DialManager) Stop() {
	m.mu.Lock()
	workers := m.workers
	m.workers = map[string]*dialWorker{}
	m.mu.Unlock()
	for name, worker := range workers {
		if worker.cancel != nil {
			worker.cancel()
		}
		worker.closePeers("server shutting down")
		m.logger.Debug("stopped dial endpoint", "endpoint", name)
	}
}

// ------------------------------------------------------------------- worker

func (m *DialManager) runWorker(ctx context.Context, worker *dialWorker) {
	minDelay := worker.spec.Reconnect.Min.Std()
	if minDelay <= 0 {
		minDelay = time.Second
	}
	maxDelay := worker.spec.Reconnect.Max.Std()
	if maxDelay <= 0 {
		maxDelay = time.Minute
	}
	jitter := worker.spec.Reconnect.Jitter
	if jitter <= 0 {
		jitter = 0.3
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		worker.setState("connecting", "")
		established, err := m.dialOnce(ctx, worker)
		if ctx.Err() != nil {
			return
		}
		if established {
			// The connection worked, so the next attempt starts from the floor.
			attempt = 0
			if err != nil {
				m.logger.Info("dialed connection ended", "endpoint", worker.spec.Name, "error", err)
			}
		} else {
			attempt++
			worker.recordFailure(err)
			m.logger.Warn("dial endpoint failed", "endpoint", worker.spec.Name,
				"attempt", attempt, "url", worker.spec.URL, "error", err)
		}

		delay := hub.Backoff(attempt, minDelay, maxDelay, jitter, nil)
		worker.setState("backoff", "")
		worker.setNextRetry(time.Now().Add(delay))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// dialOnce performs one connection attempt. established reports whether the
// connection was handed to the hub (even if it ended later).
func (m *DialManager) dialOnce(ctx context.Context, worker *dialWorker) (bool, error) {
	switch worker.spec.Kind {
	case config.KindUpstreamDial:
		if worker.spec.Mode == "split" {
			return m.dialUpstreamSplit(ctx, worker)
		}
		return m.dialUpstream(ctx, worker, dialURL(worker.spec.URL, ""), onebot.RoleUniversal)
	case config.KindDownstreamDial:
		return m.dialDownstream(ctx, worker)
	default:
		return false, fmt.Errorf("unknown endpoint kind %q", worker.spec.Kind)
	}
}

func (m *DialManager) dialUpstream(ctx context.Context, worker *dialWorker, target string, role onebot.Role) (bool, error) {
	header := m.headers(worker.spec, worker.spec.AccountHint, role)
	conn, resp, err := m.dialer.DialContext(ctx, withToken(target, worker.spec.Token), header)
	if err != nil {
		return false, dialError(resp, err)
	}

	peer := hub.NewWSPeer(conn, hub.PeerOptions{
		ID:               fmt.Sprintf("updial-%d", m.seq.Add(1)),
		Kind:             hub.KindUpstream,
		Role:             role,
		SelfID:           worker.spec.AccountHint,
		RemoteAddr:       conn.RemoteAddr().String(),
		UserAgent:        "OnebotNoa",
		TokenFingerprint: hub.FingerprintToken(worker.spec.Token),
		QueueSize:        m.cfg.Policy.WriteQueueSize,
		Backpressure:     hub.PolicyBlock,
		PingInterval:     m.cfg.Policy.PingInterval.Std(),
		PongTimeout:      m.cfg.Policy.PongTimeout.Std(),
		MaxFrameBytes:    32 << 20,
	}, m.logger)
	worker.track(peer)
	defer worker.untrack(peer)

	worker.setState("online", "")
	info := hub.UpstreamInfo{
		SelfID:             worker.spec.AccountHint,
		Role:               role,
		RemoteAddr:         conn.RemoteAddr().String(),
		UserAgent:          "OnebotNoa",
		TokenFingerprint:   hub.FingerprintToken(worker.spec.Token),
		TokenAccountSelfID: worker.spec.AccountHint,
		Source:             "endpoint:" + worker.spec.Name,
	}
	return true, m.hub.HandleUpstream(ctx, info, peer)
}

// dialUpstreamSplit runs the API and Event legs of one implementation; when one
// leg ends the other is closed so the pair always restarts together.
func (m *DialManager) dialUpstreamSplit(ctx context.Context, worker *dialWorker) (bool, error) {
	legCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		established bool
		err         error
	}
	results := make(chan outcome, 2)
	for _, role := range []onebot.Role{onebot.RoleAPI, onebot.RoleEvent} {
		go func(role onebot.Role) {
			established, err := m.dialUpstream(legCtx, worker,
				dialURL(worker.spec.URL, strings.ToLower(string(role))), role)
			results <- outcome{established: established, err: err}
		}(role)
	}

	established := false
	var firstErr error
	for i := 0; i < 2; i++ {
		result := <-results
		if result.established {
			established = true
		}
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		}
		cancel() // stop the sibling leg as soon as one of them ends
	}
	return established, firstErr
}

func (m *DialManager) dialDownstream(ctx context.Context, worker *dialWorker) (bool, error) {
	if worker.spec.BotID == 0 {
		return false, fmt.Errorf("bot %q does not exist yet: create it in the WebUI first", worker.spec.BotName)
	}
	bot, err := m.store.BotByID(ctx, worker.spec.BotID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, fmt.Errorf("bot %d no longer exists", worker.spec.BotID)
		}
		return false, err
	}
	if !bot.Enabled {
		return false, fmt.Errorf("bot %s is disabled", bot.Name)
	}

	role := onebot.RoleUniversal
	header := m.headers(worker.spec, worker.spec.FixedSelfID, role)
	conn, resp, err := m.dialer.DialContext(ctx, withToken(worker.spec.URL, worker.spec.Token), header)
	if err != nil {
		return false, dialError(resp, err)
	}

	peer := hub.NewWSPeer(conn, hub.PeerOptions{
		ID:               fmt.Sprintf("downdial-%d", m.seq.Add(1)),
		Kind:             hub.KindDownstream,
		Role:             role,
		SelfID:           worker.spec.FixedSelfID,
		RemoteAddr:       conn.RemoteAddr().String(),
		UserAgent:        "OnebotNoa",
		TokenFingerprint: hub.FingerprintToken(worker.spec.Token),
		QueueSize:        m.cfg.Policy.WriteQueueSize,
		Backpressure:     m.cfg.Policy.BotBackpressure,
		PingInterval:     m.cfg.Policy.PingInterval.Std(),
		PongTimeout:      m.cfg.Policy.PongTimeout.Std(),
		MaxFrameBytes:    16 << 20,
	}, m.logger)
	worker.track(peer)
	defer worker.untrack(peer)

	worker.setState("online", "")
	info := hub.DownstreamInfo{
		Bot:              bot,
		FixedSelfID:      worker.spec.FixedSelfID,
		RemoteAddr:       conn.RemoteAddr().String(),
		UserAgent:        "OnebotNoa",
		TokenFingerprint: hub.FingerprintToken(worker.spec.Token),
		Source:           "endpoint:" + worker.spec.Name,
	}
	return true, m.hub.HandleDownstream(ctx, info, peer)
}

// ------------------------------------------------------------------ helpers

func (m *DialManager) headers(spec EndpointSpec, selfID string, role onebot.Role) http.Header {
	header := http.Header{}
	header.Set("User-Agent", "OnebotNoa")
	header.Set("X-Client-Role", string(role))
	if selfID != "" {
		header.Set("X-Self-ID", selfID)
	}
	if spec.Token != "" {
		header.Set("Authorization", "Bearer "+spec.Token)
	}
	return header
}

// dialURL appends a role suffix to a base address ("ws://host:6700" + "/api").
func dialURL(base, suffix string) string {
	if suffix == "" {
		return base
	}
	return strings.TrimRight(base, "/") + "/" + suffix
}

// withToken adds the access_token query parameter, the other place OneBot
// implementations look for a credential.
func withToken(raw, token string) string {
	if token == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	query.Set("access_token", token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// dialError turns a failed handshake into a readable message.
func dialError(resp *http.Response, err error) error {
	if resp != nil {
		return fmt.Errorf("handshake rejected with HTTP %d", resp.StatusCode)
	}
	return err
}
