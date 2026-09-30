package transport

import (
	"net/http"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
)

// MetricsHandler serves the Prometheus text exposition for the live relay.
//
// It is mounted only when metrics are enabled in the configuration, and it
// shares the management listener, so keeping the hub on localhost (the default)
// also keeps the metrics local.
func MetricsHandler(collector *hub.Metrics, relay *hub.Hub, events *hub.EventLog, policy *hub.PolicyEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")

		var limiter *hub.Limiter
		if policy != nil {
			limiter = policy.Limiter()
		}
		var status hub.Status
		if relay != nil {
			status = relay.Status()
		}
		if err := collector.Write(w, status, events, limiter, policy); err != nil {
			// Headers are already on the wire; Prometheus reports the short body
			// as a scrape failure, which is the honest outcome here.
			return
		}
	}
}
