// Package config loads the hub configuration from YAML with sane defaults and
// environment overrides. Static, process-wide settings live here; dynamic
// entities (accounts, bots, bindings, listeners) live in the database.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so YAML can carry values like "15s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("config: duration must be a string like \"15s\": %w", err)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the root configuration document.
type Config struct {
	Server    Server     `yaml:"server"`
	OneBot    OneBot     `yaml:"onebot"`
	Policy    Policy     `yaml:"policy"`
	Storage   Storage    `yaml:"storage"`
	Metrics   Metrics    `yaml:"metrics"`
	Log       Log        `yaml:"log"`
	Endpoints []Endpoint `yaml:"endpoints"`
	Dedicated []Listener `yaml:"dedicated"`
}

// Server holds process-level HTTP settings.
type Server struct {
	Listen                 string   `yaml:"listen"`
	TLSCert                string   `yaml:"tls_cert"`
	TLSKey                 string   `yaml:"tls_key"`
	AdminBootstrapPassword string   `yaml:"admin_bootstrap_password"`
	TrustProxy             bool     `yaml:"trust_proxy"`
	ReadHeaderTimeout      Duration `yaml:"read_header_timeout"`
	ShutdownTimeout        Duration `yaml:"shutdown_timeout"`
}

// OneBot groups the data-plane settings.
type OneBot struct {
	UpstreamWS   UpstreamWS    `yaml:"upstream_ws"`
	DownstreamWS DownstreamWS  `yaml:"downstream_ws"`
	HTTP         HTTPTransport `yaml:"http"`
}

// UpstreamWS is the shared listener that QQ-side implementations dial into.
type UpstreamWS struct {
	Enable               bool     `yaml:"enable"`
	Path                 string   `yaml:"path"`
	RequireToken         bool     `yaml:"require_token"`
	AllowQueryToken      bool     `yaml:"allow_query_token"`
	BootstrapToken       string   `yaml:"bootstrap_token"`
	UnknownAccountPolicy string   `yaml:"unknown_account_policy"`
	IdentityTimeout      Duration `yaml:"identity_timeout"`
}

// DownstreamWS is the shared listener that Bot-side applications dial into.
type DownstreamWS struct {
	Enable       bool   `yaml:"enable"`
	Path         string `yaml:"path"`
	RequireToken bool   `yaml:"require_token"`
}

// HTTPTransport configures the OneBot HTTP compatibility layer.
type HTTPTransport struct {
	Enable         bool   `yaml:"enable"`
	APIPath        string `yaml:"api_path"`
	ReportPath     string `yaml:"report_path"`
	QuickOperation bool   `yaml:"quick_operation"`
	MaxBodyBytes   int64  `yaml:"max_body_bytes"`
}

// Policy holds routing, backpressure and rate-limit defaults.
type Policy struct {
	ActionTimeout       Duration  `yaml:"action_timeout"`
	StreamIdleTimeout   Duration  `yaml:"stream_idle_timeout"`
	StreamMaxTimeout    Duration  `yaml:"stream_max_timeout"`
	OfflineActionPolicy string    `yaml:"offline_action_policy"`
	Queue               Queue     `yaml:"queue"`
	BotBackpressure     string    `yaml:"bot_backpressure"`
	WriteQueueSize      int       `yaml:"write_queue_size"`
	MetaEvents          string    `yaml:"meta_events"`
	HeartbeatInterval   Duration  `yaml:"heartbeat_interval"`
	PerAccountRate      RateLimit `yaml:"per_account_rate"`
	PerBotRate          RateLimit `yaml:"per_bot_rate"`
	PendingPerConn      int       `yaml:"pending_per_conn"`
	PendingGlobal       int       `yaml:"pending_global"`
	PingInterval        Duration  `yaml:"ping_interval"`
	PongTimeout         Duration  `yaml:"pong_timeout"`
}

// Queue bounds queued actions while an upstream account is offline.
type Queue struct {
	Size int      `yaml:"size"`
	TTL  Duration `yaml:"ttl"`
}

// RateLimit is a token bucket plus an optional concurrency cap.
type RateLimit struct {
	Rate        float64 `yaml:"rate"`
	Burst       int     `yaml:"burst"`
	Concurrency int     `yaml:"concurrency"`
}

// Storage configures persistence and in-memory retention.
type Storage struct {
	SQLite     string   `yaml:"sqlite"`
	EventRing  int      `yaml:"event_ring"`
	SessionTTL Duration `yaml:"session_ttl"`
}

// Metrics configures the Prometheus text endpoint.
type Metrics struct {
	Enable bool `yaml:"enable"`
}

// Log configures the structured logger.
type Log struct {
	Level     string `yaml:"level"`
	Format    string `yaml:"format"`
	AddSource bool   `yaml:"add_source"`
}

// Endpoint is a relay-dialed connection (the hub is the WebSocket client).
type Endpoint struct {
	Name        string    `yaml:"name"`
	Kind        string    `yaml:"kind"`
	URL         string    `yaml:"url"`
	Token       string    `yaml:"token"`
	Mode        string    `yaml:"mode"`
	AccountHint string    `yaml:"account_hint"`
	BotName     string    `yaml:"bot_name"`
	Enabled     *bool     `yaml:"enabled"`
	Reconnect   Reconnect `yaml:"reconnect"`
}

// Reconnect controls dial retry backoff.
type Reconnect struct {
	Min    Duration `yaml:"min"`
	Max    Duration `yaml:"max"`
	Jitter float64  `yaml:"jitter"`
}

// Listener is a dedicated (own port/path) listener. The same shape is stored in
// the listeners table for WebUI-managed ones.
type Listener struct {
	Name          string `yaml:"name"`
	Kind          string `yaml:"kind"`
	Addr          string `yaml:"addr"`
	Path          string `yaml:"path"`
	AccountSelfID string `yaml:"account_self_id"`
	BotName       string `yaml:"bot_name"`
	FixedSelfID   string `yaml:"fixed_self_id"`
	TLSCert       string `yaml:"tls_cert"`
	TLSKey        string `yaml:"tls_key"`
	Enabled       *bool  `yaml:"enabled"`
}

// Connection kinds, shared by listeners and outbound endpoints.
const (
	KindUpstreamListen   = "upstream_listen"
	KindUpstreamDial     = "upstream_dial"
	KindDownstreamListen = "downstream_listen"
	KindDownstreamDial   = "downstream_dial"
)

// Default returns the built-in configuration used for missing keys and files.
func Default() *Config {
	return &Config{
		Server: Server{
			Listen:            "127.0.0.1:8080",
			ReadHeaderTimeout: Duration(10 * time.Second),
			ShutdownTimeout:   Duration(8 * time.Second),
		},
		OneBot: OneBot{
			UpstreamWS: UpstreamWS{
				Enable:               true,
				Path:                 "/onebot/v11/ws",
				RequireToken:         true,
				AllowQueryToken:      true,
				UnknownAccountPolicy: "pending",
				IdentityTimeout:      Duration(3 * time.Second),
			},
			DownstreamWS: DownstreamWS{
				Enable:       true,
				Path:         "/onebot/v11/bot/ws",
				RequireToken: true,
			},
			HTTP: HTTPTransport{
				Enable:       true,
				APIPath:      "/onebot/v11/http",
				ReportPath:   "/onebot/v11/report",
				MaxBodyBytes: 4 << 20,
			},
		},
		Policy: Policy{
			ActionTimeout:       Duration(15 * time.Second),
			StreamIdleTimeout:   Duration(60 * time.Second),
			StreamMaxTimeout:    Duration(10 * time.Minute),
			OfflineActionPolicy: "fail_fast",
			Queue:               Queue{Size: 256, TTL: Duration(10 * time.Second)},
			BotBackpressure:     "drop_oldest",
			WriteQueueSize:      1024,
			MetaEvents:          "synthetic",
			HeartbeatInterval:   Duration(5 * time.Second),
			PerAccountRate:      RateLimit{Rate: 1, Burst: 5},
			PerBotRate:          RateLimit{Rate: 5, Burst: 10, Concurrency: 4},
			PendingPerConn:      512,
			PendingGlobal:       8192,
			PingInterval:        Duration(30 * time.Second),
			PongTimeout:         Duration(10 * time.Second),
		},
		Storage: Storage{
			SQLite:     "./data/onebotnoa.db",
			EventRing:  2000,
			SessionTTL: Duration(7 * 24 * time.Hour),
		},
		Metrics: Metrics{Enable: true},
		Log:     Log{Level: "info", Format: "json"},
	}
}

// Load reads path (tolerating a missing file), applies environment overrides and
// validates the result. Unknown YAML keys are rejected so typos surface early.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			dec := yaml.NewDecoder(bytes.NewReader(raw))
			dec.KnownFields(true)
			if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("config: parse %s: %w", path, err)
			}
		case errors.Is(err, fs.ErrNotExist):
			// Defaults are valid on their own; the caller logs the absence.
		default:
			return nil, fmt.Errorf("config: read %s: %w", path, err)
		}
	}

	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("ONEBOTNOA_LISTEN"); v != "" {
		c.Server.Listen = v
	}
	if v := os.Getenv("ONEBOTNOA_SQLITE"); v != "" {
		c.Storage.SQLite = v
	}
	if v := os.Getenv("ONEBOTNOA_ADMIN_PASSWORD"); v != "" {
		c.Server.AdminBootstrapPassword = v
	}
	if v := os.Getenv("ONEBOTNOA_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := os.Getenv("ONEBOTNOA_LOG_FORMAT"); v != "" {
		c.Log.Format = v
	}
	if v := os.Getenv("ONEBOTNOA_TRUST_PROXY"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Server.TrustProxy = b
		}
	}
}

// Validate rejects values that would otherwise fail later and confusingly.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server.Listen) == "" {
		return errors.New("config: server.listen must not be empty")
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return errors.New("config: server.tls_cert and server.tls_key must be set together")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: log.level %q is not one of debug|info|warn|error", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		return fmt.Errorf("config: log.format %q is not one of json|text", c.Log.Format)
	}
	for _, p := range []struct{ name, value string }{
		{"onebot.upstream_ws.path", c.OneBot.UpstreamWS.Path},
		{"onebot.downstream_ws.path", c.OneBot.DownstreamWS.Path},
		{"onebot.http.api_path", c.OneBot.HTTP.APIPath},
		{"onebot.http.report_path", c.OneBot.HTTP.ReportPath},
	} {
		if !strings.HasPrefix(p.value, "/") {
			return fmt.Errorf("config: %s must start with /", p.name)
		}
	}
	switch c.OneBot.UpstreamWS.UnknownAccountPolicy {
	case "pending", "reject", "auto":
	default:
		return fmt.Errorf("config: onebot.upstream_ws.unknown_account_policy %q is not one of pending|reject|auto",
			c.OneBot.UpstreamWS.UnknownAccountPolicy)
	}
	switch c.Policy.OfflineActionPolicy {
	case "fail_fast", "queue":
	default:
		return fmt.Errorf("config: policy.offline_action_policy %q is not one of fail_fast|queue", c.Policy.OfflineActionPolicy)
	}
	switch c.Policy.BotBackpressure {
	case "drop_oldest", "drop_newest", "disconnect", "block":
	default:
		return fmt.Errorf("config: policy.bot_backpressure %q is not one of drop_oldest|drop_newest|disconnect|block", c.Policy.BotBackpressure)
	}
	switch c.Policy.MetaEvents {
	case "synthetic", "passthrough", "drop":
	default:
		return fmt.Errorf("config: policy.meta_events %q is not one of synthetic|passthrough|drop", c.Policy.MetaEvents)
	}
	if c.Policy.WriteQueueSize <= 0 {
		return errors.New("config: policy.write_queue_size must be > 0")
	}
	if c.Policy.PendingPerConn <= 0 || c.Policy.PendingGlobal <= 0 {
		return errors.New("config: policy.pending_per_conn and policy.pending_global must be > 0")
	}
	if c.Policy.PerAccountRate.Rate < 0 || c.Policy.PerBotRate.Rate < 0 {
		return errors.New("config: rate limits must not be negative")
	}
	if strings.TrimSpace(c.Storage.SQLite) == "" {
		return errors.New("config: storage.sqlite must not be empty")
	}
	for i, ep := range c.Endpoints {
		switch ep.Kind {
		case KindUpstreamDial, KindDownstreamDial:
		default:
			return fmt.Errorf("config: endpoints[%d].kind %q is not one of upstream_dial|downstream_dial", i, ep.Kind)
		}
		if strings.TrimSpace(ep.URL) == "" {
			return fmt.Errorf("config: endpoints[%d].url must not be empty", i)
		}
	}
	for i, l := range c.Dedicated {
		switch l.Kind {
		case KindUpstreamListen, KindDownstreamListen:
		default:
			return fmt.Errorf("config: dedicated[%d].kind %q is not one of upstream_listen|downstream_listen", i, l.Kind)
		}
	}
	return nil
}
