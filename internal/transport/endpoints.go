package transport

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/model"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/store"
)

// LoadEndpointSpecs assembles the desired dial targets from config.yaml and the
// database. A database row with the same name as a configured endpoint wins, so
// the WebUI can override a static entry.
func LoadEndpointSpecs(ctx context.Context, cfg *config.Config, st *store.Store, logger *slog.Logger) []EndpointSpec {
	if logger == nil {
		logger = slog.Default()
	}

	specs := specsFromConfig(cfg)
	byName := map[string]int{}
	for i, spec := range specs {
		byName[spec.Name] = i
		if spec.Kind == config.KindDownstreamDial && spec.BotID == 0 {
			// config.yaml refers to Bots by name; resolve it to the id the
			// runtime needs (and keep the name so errors stay readable).
			bot, err := st.BotByName(ctx, spec.BotName)
			if err != nil {
				logger.Warn("downstream endpoint references a Bot that does not exist yet",
					"endpoint", spec.Name, "bot", spec.BotName)
				continue
			}
			specs[i].BotID = bot.ID
			specs[i].BotName = bot.Name
		}
	}

	rows, err := st.ListEndpoints(ctx)
	if err != nil {
		logger.Warn("could not read dial endpoints from the database", "error", err)
		return specs
	}
	for _, row := range rows {
		spec, ok := specFromRow(row, st, ctx, logger)
		if !ok {
			continue
		}
		if idx, exists := byName[spec.Name]; exists {
			logger.Warn("a database endpoint overrides the configured one", "endpoint", spec.Name)
			specs[idx] = spec
			continue
		}
		byName[spec.Name] = len(specs)
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

func specsFromConfig(cfg *config.Config) []EndpointSpec {
	specs := make([]EndpointSpec, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		enabled := true
		if ep.Enabled != nil {
			enabled = *ep.Enabled
		}
		mode := ep.Mode
		if mode == "" {
			mode = "universal"
		}
		spec := EndpointSpec{
			Name:        ep.Name,
			Kind:        ep.Kind,
			URL:         ep.URL,
			Mode:        mode,
			Token:       ep.Token,
			AccountHint: ep.AccountHint,
			BotName:     ep.BotName,
			Enabled:     enabled,
			Source:      "config",
			Reconnect:   ep.Reconnect,
		}
		specs = append(specs, spec)
	}
	return specs
}

// specFromRow converts a database row, resolving the Bot name to an id.
func specFromRow(row model.Endpoint, st *store.Store, ctx context.Context, logger *slog.Logger) (EndpointSpec, bool) {
	spec := EndpointSpec{
		DBID:        row.ID,
		Name:        row.Name,
		Kind:        row.Kind,
		URL:         row.URL,
		Mode:        row.Mode,
		Token:       row.Token,
		AccountHint: row.AccountHint,
		Enabled:     row.Enabled,
		Source:      "database",
		Reconnect:   parseReconnect(row.Reconnect),
	}
	if spec.Mode == "" {
		spec.Mode = "universal"
	}
	if spec.Kind != config.KindUpstreamDial && spec.Kind != config.KindDownstreamDial {
		logger.Warn("ignoring endpoint with an unknown kind", "endpoint", spec.Name, "kind", spec.Kind)
		return spec, false
	}
	if spec.URL == "" {
		logger.Warn("ignoring endpoint without a url", "endpoint", spec.Name)
		return spec, false
	}
	if spec.Kind == config.KindDownstreamDial {
		if row.BotID == nil {
			logger.Warn("ignoring downstream endpoint without a bot", "endpoint", spec.Name)
			return spec, false
		}
		spec.BotID = *row.BotID
		if bot, err := st.BotByID(ctx, *row.BotID); err == nil {
			spec.BotName = bot.Name
		}
	}
	return spec, true
}

// parseReconnect reads the JSON column; unset values fall back to the defaults.
func parseReconnect(raw json.RawMessage) config.Reconnect {
	var wire struct {
		Min    string  `json:"min"`
		Max    string  `json:"max"`
		Jitter float64 `json:"jitter"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &wire)
	}
	var out config.Reconnect
	if d, err := time.ParseDuration(wire.Min); err == nil && d > 0 {
		out.Min = config.Duration(d)
	}
	if d, err := time.ParseDuration(wire.Max); err == nil && d > 0 {
		out.Max = config.Duration(d)
	}
	out.Jitter = wire.Jitter
	return out
}
