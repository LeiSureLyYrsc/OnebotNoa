// Package config loads process-level static configuration.
//
// Static settings live in config.yaml and are only read at startup; anything
// that must change at runtime (accounts, bots, bindings, tokens) is owned by
// the store / connect document instead.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root static configuration document.
type Config struct {
	Server  ServerConfig  `yaml:"server"`
	OneBot  OneBotConfig  `yaml:"onebot"`
	Storage StorageConfig `yaml:"storage"`
	Policy  PolicyConfig  `yaml:"policy"`
	Log     LogConfig     `yaml:"log"`
}

// ServerConfig configures the management plane and shared data-plane listener.
type ServerConfig struct {
	Listen        string `yaml:"listen"`
	AdminPassword string `yaml:"admin_password"`
	TrustProxy    bool   `yaml:"trust_proxy"`
	TLSCert       string `yaml:"tls_cert"`
	TLSKey        string `yaml:"tls_key"`
}

// OneBotConfig configures the four WebSocket data-plane modes plus HTTP.
type OneBotConfig struct {
	Upstream   ListenEndpoint `yaml:"upstream"`
	Downstream ListenEndpoint `yaml:"downstream"`
	HTTP       HTTPEndpoint   `yaml:"http"`
	// Dial lists outbound connections the relay initiates at startup.
	Dial []DialTarget `yaml:"dial"`
}

// DialTarget is an outbound WebSocket connection the relay dials.
type DialTarget struct {
	Name    string `yaml:"name"`
	Role    string `yaml:"role"` // upstream | downstream
	URL     string `yaml:"url"`
	Token   string `yaml:"token"`
	SelfID  string `yaml:"self_id"`  // upstream: account hint
	BotName string `yaml:"bot_name"` // downstream: bot to associate
	Enabled *bool  `yaml:"enabled"`
}

// ListenEndpoint configures one shared listen endpoint.
type ListenEndpoint struct {
	Enable       bool   `yaml:"enable"`
	Path         string `yaml:"path"`
	RequireToken bool   `yaml:"require_token"`
	// UnknownAccount decides what to do with an unidentified QQ connection:
	// "pending" (await approval), "reject", or "auto" (create the account).
	UnknownAccount string `yaml:"unknown_account"`
}

// HTTPEndpoint configures the OneBot V11 HTTP compatibility layer.
type HTTPEndpoint struct {
	Enable       bool   `yaml:"enable"`
	Prefix       string `yaml:"prefix"`
	RequireToken bool   `yaml:"require_token"`
}

// StorageConfig configures persistence.
type StorageConfig struct {
	SQLite      string `yaml:"sqlite"`
	ConnectFile string `yaml:"connect_file"`
	ConnectKey  string `yaml:"connect_key"`
}

// PolicyConfig configures routing and reliability defaults.
type PolicyConfig struct {
	ActionTimeout time.Duration `yaml:"action_timeout"`
	OfflineAction string        `yaml:"offline_action"` // fail_fast | queue
	MetaEvents    string        `yaml:"meta_events"`    // synthetic | passthrough | drop
	AccountRate   float64       `yaml:"account_rate"`
	AccountBurst  int           `yaml:"account_burst"`
}

// LogConfig configures logging.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default returns a configuration with sensible defaults applied.
func Default() Config {
	return Config{
		Server: ServerConfig{Listen: "127.0.0.1:8080"},
		OneBot: OneBotConfig{
			Upstream:   ListenEndpoint{Enable: true, Path: "/onebot/v11/ws", RequireToken: true, UnknownAccount: "pending"},
			Downstream: ListenEndpoint{Enable: true, Path: "/onebot/v11/bot/ws"},
			HTTP:       HTTPEndpoint{Enable: true, Prefix: "/onebot/v11/http"},
		},
		Storage: StorageConfig{
			SQLite:      "./data/onebotnoa.db",
			ConnectFile: "./data/connect.json",
			ConnectKey:  "./data/connect.json.key",
		},
		Policy: PolicyConfig{
			ActionTimeout: 15 * time.Second,
			OfflineAction: "fail_fast",
			MetaEvents:    "synthetic",
			AccountRate:   1,
			AccountBurst:  5,
		},
		Log: LogConfig{Level: "info", Format: "json"},
	}
}

// Load reads config.yaml (when present) and applies environment overrides.
// A missing file is not an error: defaults plus environment are used.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// fall through: defaults + env only
		default:
			return cfg, err
		}
	}
	cfg.applyEnv()
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("ONEBOTNOA_LISTEN"); v != "" {
		c.Server.Listen = v
	}
	if v := os.Getenv("ONEBOTNOA_ADMIN_PASSWORD"); v != "" {
		c.Server.AdminPassword = v
	}
	if v := os.Getenv("ONEBOTNOA_SQLITE"); v != "" {
		c.Storage.SQLite = v
	}
	if v := os.Getenv("ONEBOTNOA_CONNECT_FILE"); v != "" {
		c.Storage.ConnectFile = v
	}
	if v := os.Getenv("ONEBOTNOA_CONNECT_KEY"); v != "" {
		c.Storage.ConnectKey = v
	}
	if v := os.Getenv("ONEBOTNOA_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := os.Getenv("ONEBOTNOA_LOG_FORMAT"); v != "" {
		c.Log.Format = v
	}
	if v := os.Getenv("ONEBOTNOA_TRUST_PROXY"); v != "" {
		c.Server.TrustProxy = isTruthy(v)
	}
	if v := os.Getenv("ONEBOTNOA_UPSTREAM_PATH"); v != "" {
		c.OneBot.Upstream.Path = v
	}
	if v := os.Getenv("ONEBOTNOA_DOWNSTREAM_PATH"); v != "" {
		c.OneBot.Downstream.Path = v
	}
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ParsePort is a small helper used by the CLI when generating addresses.
func ParsePort(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	p, _ := strconv.Atoi(addr[i+1:])
	return p
}
