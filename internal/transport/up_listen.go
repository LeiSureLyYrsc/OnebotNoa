package transport

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/hub"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// reservedUpstreamSegments are path segments with a meaning of their own; a
// per-instance token must not shadow them.
var reservedUpstreamSegments = map[string]onebot.Role{
	"api":       onebot.RoleAPI,
	"event":     onebot.RoleEvent,
	"universal": onebot.RoleUniversal,
}

// handleUpstreamWS accepts a QQ-side implementation (reverse WebSocket).
//
// Identity resolution order:
//  1. pre-bound token (path, Authorization: Bearer, X-Access-Token, ?access_token=)
//  2. X-Self-ID header (+ X-Client-Role)
//  3. first frame carrying self_id, within onebot.upstream_ws.identity_timeout
func (d *DataPlane) handleUpstreamWS(w http.ResponseWriter, r *http.Request) {
	pathRole, pathToken := parseUpstreamPath(r.PathValue("rest"))

	role := onebot.RoleUniversal
	if pathRole != "" {
		role = pathRole
	}
	if header := strings.TrimSpace(r.Header.Get("X-Client-Role")); header != "" {
		role = onebot.ParseRole(header)
	}

	token, tokenSource := d.presentedToken(r, pathToken)
	selfID := strings.TrimSpace(r.Header.Get("X-Self-ID"))
	remoteAddr := d.remoteAddr(r)

	tokenFP := hub.FingerprintToken(token)
	boundSelfID := ""
	if token != "" {
		if account, err := d.store.AccountByTokenHash(r.Context(), auth.HashToken(token)); err == nil {
			boundSelfID = account.SelfID
		}
	}

	// A dedicated listener narrows who may connect; the shared endpoint keeps
	// the configured policy.
	if binding, dedicated := bindingOf(r.Context()); dedicated {
		switch d.authorizeUpstreamListener(binding, token, boundSelfID, selfID) {
		case listenerAuthNoCredential:
			d.logger.Warn("dedicated upstream listener rejected a connection",
				"listener", binding.Name, "addr", remoteAddr, "self_id", selfID, "token_source", tokenSource)
			http.Error(w, "unauthorized for this listener", http.StatusUnauthorized)
			return
		case listenerAuthForeignAccount:
			d.logger.Warn("dedicated upstream listener rejected a foreign self_id",
				"listener", binding.Name, "wanted", binding.AccountHint, "got", selfID)
			http.Error(w, "wrong self_id for this listener", http.StatusForbidden)
			return
		}
		// The listener decides the account, so a connection cannot claim another.
		if selfID == "" {
			selfID = binding.AccountHint
		}
	} else if d.cfg.OneBot.UpstreamWS.RequireToken && !d.authorizedUpstream(token, boundSelfID) {
		d.logger.Warn("upstream connection rejected: bad or missing token",
			"addr", remoteAddr, "self_id", selfID, "token_source", tokenSource)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := d.upgrader.Upgrade(w, r, nil)
	if err != nil {
		d.logger.Warn("websocket upgrade failed", "addr", remoteAddr, "error", err)
		return
	}

	peer := hub.NewWSPeer(conn, hub.PeerOptions{
		ID:               d.nextConnID("up"),
		Kind:             hub.KindUpstream,
		Role:             role,
		SelfID:           selfID,
		RemoteAddr:       remoteAddr,
		UserAgent:        r.UserAgent(),
		TokenFingerprint: tokenFP,
		QueueSize:        d.cfg.Policy.WriteQueueSize,
		// Never silently drop frames on the way to a QQ implementation: a
		// dropped action response would hang a Bot until its timeout.
		Backpressure:  "block",
		PingInterval:  d.cfg.Policy.PingInterval.Std(),
		PongTimeout:   d.cfg.Policy.PongTimeout.Std(),
		MaxFrameBytes: 32 << 20,
	}, d.logger)

	d.track(peer)
	defer d.untrack(peer)

	source := "upstream_ws"
	if binding, dedicated := bindingOf(r.Context()); dedicated {
		source = "listener:" + binding.Name
		// The listener is authoritative: it pre-binds the account so the relay
		// never has to guess from headers.
		boundSelfID = binding.AccountHint
	}
	info := hub.UpstreamInfo{
		SelfID:             selfID,
		Role:               role,
		RemoteAddr:         remoteAddr,
		UserAgent:          r.UserAgent(),
		TokenFingerprint:   tokenFP,
		TokenAccountSelfID: boundSelfID,
		Source:             source,
	}

	d.logger.Info("upstream connection accepted",
		"conn", peer.ID(), "addr", remoteAddr, "role", string(role),
		"self_id", selfID, "token_source", tokenSource)

	if err := d.hub.HandleUpstream(r.Context(), info, peer); err != nil {
		reason := upstreamCloseReason(err)
		d.logger.Info("upstream connection finished", "conn", peer.ID(), "reason", reason)
		peer.Close(websocket.ClosePolicyViolation, reason)
	}
}

// authorizedUpstream reports whether the presented token may open a QQ-side
// connection.
func (d *DataPlane) authorizedUpstream(token, boundSelfID string) bool {
	if token == "" {
		return false
	}
	if boundSelfID != "" {
		return true // pre-bound per-instance token
	}
	return d.isBootstrapToken(token)
}

func (d *DataPlane) isBootstrapToken(token string) bool {
	want := d.cfg.OneBot.UpstreamWS.BootstrapToken
	if want == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// presentedToken extracts the access token from every place OneBot
// implementations put one, returning its source for logging.
func (d *DataPlane) presentedToken(r *http.Request, pathToken string) (string, string) {
	if pathToken != "" {
		return pathToken, "path"
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:]), "authorization"
		}
	}
	if h := strings.TrimSpace(r.Header.Get("X-Access-Token")); h != "" {
		return h, "x-access-token"
	}
	if d.cfg.OneBot.UpstreamWS.AllowQueryToken {
		if q := strings.TrimSpace(r.URL.Query().Get("access_token")); q != "" {
			return q, "query"
		}
	}
	return "", ""
}

// parseUpstreamPath interprets the optional path suffix of the shared endpoint:
//
//	/api | /event | /universal   force the client role
//	/<token>                     pre-bind the instance token
//	/<token>/<role>              both
func parseUpstreamPath(rest string) (onebot.Role, string) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	if role, ok := reservedUpstreamSegments[strings.ToLower(parts[0])]; ok {
		return role, ""
	}
	token := parts[0]
	if len(parts) > 1 {
		if role, ok := reservedUpstreamSegments[strings.ToLower(parts[1])]; ok {
			return role, token
		}
	}
	return "", token
}

// upstreamCloseReason maps an attach error to a WebSocket close reason.
func upstreamCloseReason(err error) string {
	switch {
	case errors.Is(err, hub.ErrPendingApproval):
		return "account awaiting approval in the hub WebUI"
	case errors.Is(err, hub.ErrUnknownAccount):
		return "unknown account"
	case errors.Is(err, hub.ErrAccountDisabled):
		return "account disabled"
	case errors.Is(err, hub.ErrTokenMismatch):
		return "token mismatch for this self_id"
	case errors.Is(err, hub.ErrNeedIdentity):
		return "identity timeout: no X-Self-ID header and no self_id frame"
	default:
		return "connection closed"
	}
}
