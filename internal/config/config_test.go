package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("missing file must not fail: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Fatalf("unexpected default listen: %q", cfg.Server.Listen)
	}
	if cfg.Policy.PerAccountRate.Rate != 1 || cfg.Policy.PerAccountRate.Burst != 5 {
		t.Fatalf("unexpected default account rate limit: %+v", cfg.Policy.PerAccountRate)
	}
}

func TestLoadMergesOverridesOnTopOfDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "policy:\n  action_timeout: 25s\n  per_account_rate:\n    rate: 2\nstorage:\n  sqlite: ./x.db\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Policy.ActionTimeout.Std(); got != 25*time.Second {
		t.Fatalf("action_timeout = %v, want 25s", got)
	}
	if got := cfg.Policy.PerAccountRate; got.Rate != 2 || got.Burst != 5 {
		t.Fatalf("per_account_rate = %+v, want rate 2 burst 5 (burst keeps default)", got)
	}
	if cfg.Storage.SQLite != "./x.db" {
		t.Fatalf("sqlite = %q", cfg.Storage.SQLite)
	}
	if cfg.Policy.PendingGlobal != 8192 {
		t.Fatalf("untouched defaults must survive: pending_global = %d", cfg.Policy.PendingGlobal)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("policcy:\n  action_timeout: 1s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a typo in a top-level key must be rejected")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := map[string]func(*Config){
		"empty listen":     func(c *Config) { c.Server.Listen = "" },
		"half tls":         func(c *Config) { c.Server.TLSCert = "/tmp/cert.pem" },
		"bad log level":    func(c *Config) { c.Log.Level = "verbose" },
		"relative path":    func(c *Config) { c.OneBot.UpstreamWS.Path = "onebot/v11/ws" },
		"bad account pol":  func(c *Config) { c.OneBot.UpstreamWS.UnknownAccountPolicy = "maybe" },
		"bad backpressure": func(c *Config) { c.Policy.BotBackpressure = "explode" },
		"bad meta events":  func(c *Config) { c.Policy.MetaEvents = "sometimes" },
		"zero queue":       func(c *Config) { c.Policy.WriteQueueSize = 0 },
		"empty sqlite":     func(c *Config) { c.Storage.SQLite = "" },
		"bad endpoint": func(c *Config) {
			c.Endpoints = []Endpoint{{Name: "x", Kind: "sideways", URL: "ws://x"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("%s: expected validation error", name)
			}
		})
	}
}
