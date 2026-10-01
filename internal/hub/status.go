package hub

// Account status values, mirrored from internal/model so the hub does not have
// to depend on the persisted-entity package for four strings.
const (
	StatusOffline    = "offline"
	StatusConnecting = "connecting"
	StatusOnline     = "online"
	StatusDegraded   = "degraded"
)
