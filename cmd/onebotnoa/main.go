// Command onebotnoa is the OneBot V11 relay hub: a management plane (WebUI +
// REST) with a data plane that faces QQ-side implementations on one side and
// Bot-side applications on the other.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/api"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/logging"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/transport"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `onebotnoa - OneBot V11 relay hub

Usage:
  onebotnoa serve [-config config.yaml]              start the hub (REST + WebUI + data plane)
  onebotnoa reset-password [-config ...] [-user admin] [-password secret]
  onebotnoa version                                  print version information
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "reset-password":
		err = runResetPassword(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("onebotnoa %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	if _, statErr := os.Stat(*configPath); errors.Is(statErr, os.ErrNotExist) {
		logger.Warn("config file not found, using built-in defaults", "path", *configPath)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Storage.SQLite)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	logger.Info("database ready", "path", st.Path())

	generated, err := auth.EnsureAdmin(ctx, st, cfg.Server.AdminBootstrapPassword, logger)
	if err != nil {
		return err
	}
	if generated != "" {
		logger.Warn("created the initial administrator account - store this password now and change it after login",
			"username", "admin", "password", generated)
	}

	authManager := auth.NewManager(st, cfg.Storage.SessionTTL.Std(), logger)
	go runSessionCleanup(ctx, authManager, logger)

	relay := hub.New(cfg, st, logger)

	// Live view (SSE ring), metrics and the policy engine all hang off the hub.
	eventLog := hub.NewEventLog(cfg.Storage.EventRing)
	metricsCollector := hub.NewMetrics()
	policyEngine := hub.NewPolicyEngine(cfg, logger)
	relay.SetObserver(hub.FanOutObserver{eventLog, metricsCollector})
	relay.Actions().SetTrafficObserver(hub.FanOutTraffic{eventLog, metricsCollector})
	relay.Actions().SetPreSend(policyEngine)
	// Actions buffered while an account was offline are flushed on reconnect.
	relay.SetConnectHook(func(selfID string) {
		policyEngine.FlushOffline(selfID, func(frame []byte) bool {
			return relay.SendActionTo(selfID, frame)
		})
	})
	go runPolicySweep(ctx, policyEngine, logger)

	dataPlane := transport.NewDataPlane(cfg, st, relay, logger)

	// Outbound connections: the hub dials QQ implementations and Bot servers.
	dialManager := transport.NewDialManager(cfg, st, relay, logger)
	dialManager.Start(ctx, transport.LoadEndpointSpecs(ctx, cfg, st, logger))

	startedAt := time.Now()
	mux := transport.NewMux(transport.Options{Version: version, StartedAt: startedAt, Logger: logger})
	dataPlane.Register(mux)
	if cfg.Metrics.Enable {
		mux.HandleFunc("GET /metrics", transport.MetricsHandler(metricsCollector, relay, eventLog, policyEngine))
	}
	api.New(api.Options{
		Store:     st,
		Auth:      authManager,
		Logger:    logger,
		Config:    cfg,
		Hub:       relay,
		Events:    eventLog,
		Metrics:   metricsCollector,
		Policy:    policyEngine,
		Dialer:    dialManager,
		Version:   version,
		StartedAt: startedAt,
	}).Register(mux)

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		scheme := "http"
		if cfg.Server.TLSCert != "" {
			scheme = "https"
		}
		logger.Info("hub listening",
			"addr", cfg.Server.Listen,
			"url", scheme+"://"+displayAddr(cfg.Server.Listen),
			"version", version,
			"onebot_upstream_ws", cfg.OneBot.UpstreamWS.Path,
			"onebot_downstream_ws", cfg.OneBot.DownstreamWS.Path,
		)
		if cfg.Server.TLSCert != "" {
			errCh <- srv.ListenAndServeTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
			return
		}
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections",
			"timeout", cfg.Server.ShutdownTimeout.Std().String())
	}

	dialManager.Stop()
	dataPlane.CloseAll("server shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.Std())
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// runPolicySweep prunes idle token buckets and expired queued actions.
func runPolicySweep(ctx context.Context, policy *hub.PolicyEngine, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if removed := policy.Limiter().Sweep(15 * time.Minute); removed > 0 {
				logger.Debug("pruned idle rate-limit buckets", "count", removed)
			}
			if dropped := policy.SweepOffline(); dropped > 0 {
				logger.Info("dropped expired queued actions", "count", dropped)
			}
		}
	}
}

// runSessionCleanup drops expired management sessions periodically.
func runSessionCleanup(ctx context.Context, manager *auth.Manager, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := manager.Cleanup(ctx)
			if err != nil {
				logger.Warn("session cleanup failed", "error", err)
				continue
			}
			if n > 0 {
				logger.Info("removed expired sessions", "count", n)
			}
		}
	}
}

// displayAddr turns ":8080" into something clickable in the log.
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

// runResetPassword sets a new management password (generating one when asked).
func runResetPassword(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML configuration file")
	username := fs.String("user", "admin", "user name to reset")
	password := fs.String("password", "", "new password (empty = generate a random one)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log)
	if err != nil {
		return err
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Storage.SQLite)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	plain := *password
	if plain == "" {
		plain = auth.NewSecret()
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		return err
	}

	user, err := st.UserByUsername(ctx, *username)
	switch {
	case err == nil:
		if err := st.UpdateUserPassword(ctx, user.ID, hash); err != nil {
			return err
		}
		if _, err := st.DeleteSessionsForUser(ctx, user.ID); err != nil {
			logger.Warn("could not drop existing sessions", "error", err)
		}
	case errors.Is(err, store.ErrNotFound):
		if _, err := st.CreateUser(ctx, *username, hash, "admin"); err != nil {
			return err
		}
	default:
		return err
	}

	if err := st.AppendAudit(ctx, model.AuditEntry{
		At: time.Now(), Actor: "cli", Action: "auth.password.reset", Target: *username,
	}); err != nil {
		logger.Warn("could not write audit entry", "error", err)
	}

	fmt.Printf("password for %q: %s\n", *username, plain)
	if *password == "" {
		fmt.Println("store it now: it is not saved anywhere in plaintext")
	}
	return nil
}
