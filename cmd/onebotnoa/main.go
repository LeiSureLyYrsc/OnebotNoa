// Command onebotnoa is the OneBot V11 relay management server.
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/api"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/connect"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/transport"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/webui"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "reset-password":
		if err := runResetPassword(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `OnebotNoa %s - OneBot V11 relay hub

usage:
  onebotnoa serve [-config config.yaml]
  onebotnoa reset-password [-config config.yaml] [-user admin] [-password NEW]
  onebotnoa version
`, version)
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to config.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.Log)
	hub.BuildVersion = version

	if err := ensureParentDirs(cfg.Storage.SQLite, cfg.Storage.ConnectFile, cfg.Storage.ConnectKey); err != nil {
		return err
	}

	st, err := store.Open(cfg.Storage.SQLite)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	vault, err := connect.Open(cfg.Storage.ConnectFile, cfg.Storage.ConnectKey)
	if err != nil {
		return fmt.Errorf("open connect vault: %w", err)
	}

	h := hub.New(st, log, hub.Options{
		ActionTimeout:  cfg.Policy.ActionTimeout,
		OfflineAction:  cfg.Policy.OfflineAction,
		MetaEvents:     cfg.Policy.MetaEvents,
		AccountRate:    cfg.Policy.AccountRate,
		AccountBurst:   cfg.Policy.AccountBurst,
		UnknownAccount: cfg.OneBot.Upstream.UnknownAccount,
	})
	if err := h.Reload(); err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	defer h.Close()

	tr := transport.New(cfg, h, st, log)
	apiSrv := api.New(cfg, h, st, vault, log, version, tr.SyncListeners)

	password, err := apiSrv.Bootstrap()
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if password != "" {
		log.Warn("generated administrator password", "username", "admin", "password", password)
	}

	mux := http.NewServeMux()
	apiSrv.Register(mux)
	tr.Register(mux)
	mux.Handle("/", webui.Handler())

	tr.Start()
	defer tr.Close()

	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Info("OnebotNoa listening", "addr", cfg.Server.Listen, "version", version)
		var serveErr error
		if cfg.Server.TLSCert != "" && cfg.Server.TLSKey != "" {
			serveErr = httpSrv.ListenAndServeTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
		} else {
			serveErr = httpSrv.ListenAndServe()
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Error("http server stopped", "err", serveErr)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(ctx)
}

func runResetPassword(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to config.yaml")
	username := fs.String("user", "admin", "username")
	password := fs.String("password", "", "new password (random when empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Storage.SQLite)
	if err != nil {
		return err
	}
	defer st.Close()

	user, err := st.GetUserByUsername(*username)
	if err != nil {
		return fmt.Errorf("user %q not found", *username)
	}
	if *password == "" {
		tok, _, err := auth.NewToken()
		if err != nil {
			return err
		}
		*password = tok
	}
	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}
	if err := st.UpdatePassword(user.ID, hash); err != nil {
		return err
	}
	if err := st.DeleteSessionsByUser(user.ID); err != nil {
		return err
	}
	fmt.Printf("password reset for %s: %s\n", user.Username, *password)
	return nil
}

func ensureParentDirs(paths ...string) error {
	for _, p := range paths {
		dir := filepath.Dir(p)
		if dir == "" || dir == "." {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
