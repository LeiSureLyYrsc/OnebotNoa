package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// ListenerSpec is one dedicated data-plane listener (config.yaml or database).
type ListenerSpec struct {
	DBID         int64
	Name         string
	Kind         string
	Addr         string
	Path         string
	AccountID    *int64
	AccountHint  string
	BotID        int64
	BotName      string
	FixedSelfID  string
	TLSCert      string
	TLSKey       string
	Enabled      bool
	Source       string
}

// ListenerState is the runtime view of a dedicated listener for the WebUI.
type ListenerState struct {
	DBID        int64  `json:"db_id,omitempty"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Addr        string `json:"addr"`
	Path        string `json:"path"`
	AccountHint string `json:"account_hint,omitempty"`
	BotName     string `json:"bot_name,omitempty"`
	FixedSelfID string `json:"fixed_self_id,omitempty"`
	TLS         bool   `json:"tls"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
	State       string `json:"state"`
	LastError   string `json:"last_error,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	URL         string `json:"url,omitempty"`
}

// listenerInstance is one running http.Server.
type listenerInstance struct {
	spec   ListenerSpec
	server *http.Server
	state  ListenerState

	mu sync.Mutex
}

// ListenerManager owns the dedicated listeners: it binds their sockets, serves
// the data plane on them and rebuilds them when the configuration changes.
//
// Dedicated listeners exist so one account (or one Bot) can have its own
// address, which is what makes a mixed deployment possible: an implementation
// that must post to a fixed URL, a Bot that expects its own port, and the shared
// endpoints can all coexist.
type ListenerManager struct {
	cfg        *config.Config
	store      *store.Store
	dataPlane  *DataPlane
	logger     *slog.Logger

	mu        sync.Mutex
	instances map[string]*listenerInstance
	ctx       context.Context
}

// NewListenerManager builds the manager.
func NewListenerManager(cfg *config.Config, st *store.Store, dp *DataPlane, logger *slog.Logger) *ListenerManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &ListenerManager{
		cfg: cfg, store: st, dataPlane: dp, logger: logger,
		instances: map[string]*listenerInstance{},
	}
}

// Start applies the initial listener set.
func (m *ListenerManager) Start(ctx context.Context, specs []ListenerSpec) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	m.Apply(specs)
}

// Reload re-reads config.yaml plus the database and applies the result.
func (m *ListenerManager) Reload(ctx context.Context) {
	m.Apply(LoadListenerSpecs(ctx, m.cfg, m.store, m.logger))
}

// Apply reconciles the running listeners with the desired set: unchanged specs
// keep their socket, changed or removed ones are shut down, new ones are bound.
func (m *ListenerManager) Apply(specs []ListenerSpec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return
	}

	desired := map[string]ListenerSpec{}
	for _, spec := range specs {
		desired[spec.Name] = spec
	}

	for name, instance := range m.instances {
		spec, keep := desired[name]
		if keep && instance.spec == spec {
			continue
		}
		m.logger.Info("stopping dedicated listener", "listener", name, "replaced", keep)
		m.shutdownLocked(instance)
		delete(m.instances, name)
	}

	for name, spec := range desired {
		if _, running := m.instances[name]; running {
			continue
		}
		instance := &listenerInstance{spec: spec}
		if !spec.Enabled {
			instance.state = stateFromSpec(spec)
			instance.state.State = "disabled"
			m.instances[name] = instance
			continue
		}
		if err := m.bindLocked(instance); err != nil {
			instance.state = stateFromSpec(spec)
			instance.state.State = "error"
			instance.state.LastError = err.Error()
			m.instances[name] = instance
			m.logger.Warn("dedicated listener could not start",
				"listener", name, "addr", spec.Addr, "error", err)
			continue
		}
		m.instances[name] = instance
		m.logger.Info("dedicated listener started",
			"listener", name, "kind", spec.Kind, "addr", spec.Addr, "path", spec.Path)
	}
}

// bindLocked binds the socket and starts serving in the background.
func (m *ListenerManager) bindLocked(instance *listenerInstance) error {
	spec := instance.spec
	if spec.Addr == "" {
		return errors.New("缺少监听地址")
	}
	if spec.Path == "" {
		spec.Path = defaultListenerPath(spec.Kind)
	}

	mux := http.NewServeMux()
	if err := m.dataPlane.registerOn(mux, spec); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              spec.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		ErrorLog:          slog.NewLogLogger(m.logger.Handler(), slog.LevelWarn),
	}
	if spec.TLSCert != "" && spec.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(spec.TLSCert, spec.TLSKey)
		if err != nil {
			return fmt.Errorf("加载 TLS 证书失败: %w", err)
		}
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	// Bind explicitly so a port conflict is reported instead of crashing later.
	listener, err := net.Listen("tcp", spec.Addr)
	if err != nil {
		return err
	}

	instance.server = server
	instance.state = stateFromSpec(spec)
	instance.state.State = "listening"
	instance.state.StartedAt = time.Now().Format(time.RFC3339)

	useTLS := server.TLSConfig != nil
	go func() {
		var serveErr error
		if useTLS {
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			instance.mu.Lock()
			instance.state.State = "error"
			instance.state.LastError = serveErr.Error()
			instance.mu.Unlock()
			m.logger.Error("dedicated listener stopped", "listener", spec.Name, "error", serveErr)
		}
	}()
	return nil
}

// shutdownLocked stops one listener and waits briefly for in-flight requests.
func (m *ListenerManager) shutdownLocked(instance *listenerInstance) {
	if instance.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := instance.server.Shutdown(ctx); err != nil {
		_ = instance.server.Close()
	}
	instance.mu.Lock()
	instance.state.State = "stopped"
	instance.mu.Unlock()
}

// States returns the runtime view of every dedicated listener, sorted by name.
func (m *ListenerManager) States() []ListenerState {
	m.mu.Lock()
	instances := make([]*listenerInstance, 0, len(m.instances))
	for _, instance := range m.instances {
		instances = append(instances, instance)
	}
	m.mu.Unlock()

	out := make([]ListenerState, 0, len(instances))
	for _, instance := range instances {
		instance.mu.Lock()
		state := instance.state
		instance.mu.Unlock()
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Stop shuts every listener down.
func (m *ListenerManager) Stop() {
	m.mu.Lock()
	instances := m.instances
	m.instances = map[string]*listenerInstance{}
	m.mu.Unlock()
	for name, instance := range instances {
		m.shutdownLocked(instance)
		m.logger.Debug("stopped dedicated listener", "listener", name)
	}
}

func stateFromSpec(spec ListenerSpec) ListenerState {
	scheme := "ws"
	if spec.TLSCert != "" && spec.TLSKey != "" {
		scheme = "wss"
	}
	host := spec.Addr
	if strings.HasPrefix(host, "0.0.0.0:") || strings.HasPrefix(host, "[::]:") {
		host = "127.0.0.1:" + strings.TrimPrefix(strings.TrimPrefix(host, "0.0.0.0:"), "[::]:")
	}
	path := spec.Path
	if path == "" {
		path = defaultListenerPath(spec.Kind)
	}
	return ListenerState{
		DBID:        spec.DBID,
		Name:        spec.Name,
		Kind:        spec.Kind,
		Addr:        spec.Addr,
		Path:        path,
		AccountHint: spec.AccountHint,
		BotName:     spec.BotName,
		FixedSelfID: spec.FixedSelfID,
		TLS:         scheme == "wss",
		Enabled:     spec.Enabled,
		Source:      spec.Source,
		State:       "idle",
		URL:         scheme + "://" + host + path,
	}
}

// defaultListenerPath is the OneBot convention for each kind.
func defaultListenerPath(kind string) string {
	if kind == config.KindDownstreamListen {
		return "/onebot/v11/bot/ws"
	}
	return "/onebot/v11/ws"
}

// LoadListenerSpecs assembles the desired dedicated listeners from config.yaml
// and the database (a database row with the same name wins).
func LoadListenerSpecs(ctx context.Context, cfg *config.Config, st *store.Store, logger *slog.Logger) []ListenerSpec {
	if logger == nil {
		logger = slog.Default()
	}
	specs := []ListenerSpec{}
	byName := map[string]int{}

	for _, raw := range cfg.Dedicated {
		enabled := true
		if raw.Enabled != nil {
			enabled = *raw.Enabled
		}
		spec := ListenerSpec{
			Name: raw.Name, Kind: raw.Kind, Addr: raw.Addr, Path: raw.Path,
			AccountHint: raw.AccountSelfID, BotName: raw.BotName,
			FixedSelfID: raw.FixedSelfID, TLSCert: raw.TLSCert, TLSKey: raw.TLSKey,
			Enabled: enabled, Source: "config",
		}
		spec = resolveListenerSpec(ctx, st, spec, logger)
		byName[spec.Name] = len(specs)
		specs = append(specs, spec)
	}

	rows, err := st.ListListeners(ctx)
	if err != nil {
		logger.Warn("could not read dedicated listeners from the database", "error", err)
		return specs
	}
	for _, row := range rows {
		spec := ListenerSpec{
			DBID: row.ID, Name: row.Name, Kind: row.Kind, Addr: row.BindAddr, Path: row.Path,
			AccountID: row.AccountID, BotID: valueOrZero(row.BotID), FixedSelfID: row.FixedSelfID,
			TLSCert: row.TLSCert, TLSKey: row.TLSKey, Enabled: row.Enabled, Source: "database",
		}
		spec = resolveListenerSpec(ctx, st, spec, logger)
		if idx, exists := byName[spec.Name]; exists {
			logger.Warn("a database listener overrides the configured one", "listener", spec.Name)
			specs[idx] = spec
			continue
		}
		byName[spec.Name] = len(specs)
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// resolveListenerSpec fills in the account hint and Bot name the runtime needs.
func resolveListenerSpec(ctx context.Context, st *store.Store, spec ListenerSpec, logger *slog.Logger) ListenerSpec {
	if spec.Kind == config.KindDownstreamListen {
		// Resolve in both directions: config.yaml names the Bot, the database
		// stores its id, and the runtime view should always show the name.
		if spec.BotID == 0 && spec.BotName != "" {
			bot, err := st.BotByName(ctx, spec.BotName)
			if err != nil {
				logger.Warn("dedicated listener references a Bot that does not exist",
					"listener", spec.Name, "bot", spec.BotName)
				return spec
			}
			spec.BotID = bot.ID
			spec.BotName = bot.Name
			return spec
		}
		if spec.BotID != 0 {
			if bot, err := st.BotByID(ctx, spec.BotID); err == nil {
				spec.BotName = bot.Name
			} else {
				logger.Warn("dedicated listener references a Bot that no longer exists",
					"listener", spec.Name, "bot_id", spec.BotID)
			}
		}
		return spec
	}
	if spec.AccountHint == "" && spec.AccountID != nil {
		if account, err := st.AccountByID(ctx, *spec.AccountID); err == nil {
			spec.AccountHint = account.SelfID
		}
	}
	return spec
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

var _ = model.Listener{}
