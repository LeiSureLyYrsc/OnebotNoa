// Package logging builds the process-wide structured logger.
package logging

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
)

// New returns a slog logger for the given configuration.
func New(cfg config.Log) (*slog.Logger, error) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("logging: unknown level %q", cfg.Level)
	}

	opts := &slog.HandlerOptions{Level: level, AddSource: cfg.AddSource}
	var handler slog.Handler
	switch cfg.Format {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		return nil, fmt.Errorf("logging: unknown format %q", cfg.Format)
	}
	return slog.New(handler), nil
}
