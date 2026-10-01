package transport

import (
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
)

// LoadEndpointSpecs builds the desired dial targets from connect.json.
//
// Only dial kinds are returned: the file also holds the dedicated listeners,
// which the ListenerManager consumes, so each manager reads exactly the half it
// owns and neither has to filter the other's entries out of a merged list.
func LoadEndpointSpecs(conns ConnStore, logger *slog.Logger) []EndpointSpec {
	if logger == nil {
		logger = slog.Default()
	}
	if conns == nil {
		return []EndpointSpec{}
	}

	specs := []EndpointSpec{}
	for _, connection := range conns.Connections() {
		if connection.Kind != config.KindUpstreamDial && connection.Kind != config.KindDownstreamDial {
			continue
		}
		if connection.URL == "" {
			logger.Warn("ignoring a dial connection without a url", "connection", connection.Name)
			continue
		}
		spec := EndpointSpec{
			DBID:        connection.ID,
			Name:        connection.Name,
			Kind:        connection.Kind,
			URL:         connection.URL,
			Mode:        defaultString(connection.Mode, "universal"),
			AccountHint: connection.AccountSelfID,
			FixedSelfID: connection.FixedSelfID,
			Enabled:     connection.Enabled,
			Source:      SourceFile,
			Reconnect:   reconnectFrom(connection),
		}
		if spec.Kind == config.KindDownstreamDial {
			if connection.BotName == "" {
				logger.Warn("ignoring a downstream dial connection without a Bot", "connection", connection.Name)
				continue
			}
			bot, ok := conns.BotByName(connection.BotName)
			if !ok {
				logger.Warn("downstream dial connection references a Bot that does not exist",
					"connection", connection.Name, "bot", connection.BotName)
				continue
			}
			spec.BotID = bot.ID
			spec.BotName = bot.Name
		}
		if token, err := conns.ConnectionToken(connection.ID); err == nil {
			spec.Token = token
		}
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// LoadListenerSpecs builds the desired dedicated listeners from connect.json.
func LoadListenerSpecs(conns ConnStore, logger *slog.Logger) []ListenerSpec {
	if logger == nil {
		logger = slog.Default()
	}
	if conns == nil {
		return []ListenerSpec{}
	}

	specs := []ListenerSpec{}
	for _, connection := range conns.Connections() {
		if connection.Kind != config.KindUpstreamListen && connection.Kind != config.KindDownstreamListen {
			continue
		}
		spec := ListenerSpec{
			DBID:        connection.ID,
			Name:        connection.Name,
			Kind:        connection.Kind,
			Addr:        connection.Addr,
			Path:        connection.Path,
			AccountHint: connection.AccountSelfID,
			BotName:     connection.BotName,
			FixedSelfID: connection.FixedSelfID,
			TLSCert:     connection.TLSCert,
			TLSKey:      connection.TLSKey,
			Enabled:     connection.Enabled,
			Source:      SourceFile,
		}
		spec = resolveListenerSpec(conns, spec, logger)
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// resolveListenerSpec fills in the names the runtime view needs. The document
// already stores names, but a hand-written file may use an id instead, so both
// directions are resolved here.
func resolveListenerSpec(conns ConnStore, spec ListenerSpec, logger *slog.Logger) ListenerSpec {
	if spec.Kind == config.KindDownstreamListen {
		if spec.BotID == 0 && spec.BotName != "" {
			bot, ok := conns.BotByName(spec.BotName)
			if !ok {
				logger.Warn("dedicated listener references a Bot that does not exist",
					"listener", spec.Name, "bot", spec.BotName)
				return spec
			}
			spec.BotID = bot.ID
			spec.BotName = bot.Name
			return spec
		}
		if spec.BotID != 0 && spec.BotName == "" {
			if bot, ok := conns.BotByID(spec.BotID); ok {
				spec.BotName = bot.Name
			} else {
				logger.Warn("dedicated listener references a Bot that no longer exists",
					"listener", spec.Name, "bot_id", spec.BotID)
			}
		}
		return spec
	}
	if spec.AccountHint != "" {
		if _, ok := conns.AccountBySelfID(spec.AccountHint); !ok {
			logger.Warn("dedicated listener references an account that does not exist",
				"listener", spec.Name, "account_self_id", spec.AccountHint)
		}
	}
	return spec
}

// reconnectFrom reads the optional backoff schedule of a dial connection.
func reconnectFrom(connection ConnectionRef) config.Reconnect {
	var out config.Reconnect
	if parsed, err := time.ParseDuration(connection.Min); err == nil && parsed > 0 {
		out.Min = config.Duration(parsed)
	}
	if parsed, err := time.ParseDuration(connection.Max); err == nil && parsed > 0 {
		out.Max = config.Duration(parsed)
	}
	out.Jitter = connection.Jitter
	return out
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// SourceFile labels runtime entries that come from connect.json; the API shows
// it so an operator knows where to edit them.
const SourceFile = "connect.json"

var _ = strconv.Itoa
