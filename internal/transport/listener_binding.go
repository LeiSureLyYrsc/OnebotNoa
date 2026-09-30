package transport

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/auth"
	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
)

// listenerBinding is the constraint a dedicated listener imposes on the
// connections it serves: a fixed account, a fixed Bot, or both.
//
// It travels in the request context so the shared handlers keep one code path:
// a dedicated port is a stricter *view* of the same data plane, not a second
// implementation of it.
type listenerBinding struct {
	Name        string
	Kind        string
	AccountHint string
	BotID       int64
	BotName     string
	FixedSelfID string
}

type listenerBindingKey struct{}

// registerOn mounts one dedicated listener's endpoints on its own mux.
func (d *DataPlane) registerOn(mux *http.ServeMux, spec ListenerSpec) error {
	path := spec.Path
	if path == "" {
		path = defaultListenerPath(spec.Kind)
	}
	binding := listenerBinding{
		Name: spec.Name, Kind: spec.Kind,
		AccountHint: spec.AccountHint, BotID: spec.BotID, BotName: spec.BotName,
		FixedSelfID: spec.FixedSelfID,
	}
	handler := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), listenerBindingKey{}, binding)))
		}
	}

	switch spec.Kind {
	case config.KindUpstreamListen:
		if spec.AccountHint == "" {
			return fmt.Errorf("上游独立监听 %s 必须指定账号（account_hint）", spec.Name)
		}
		mux.HandleFunc("GET "+path, handler(d.handleUpstreamWS))
		mux.HandleFunc("GET "+path+"/{rest...}", handler(d.handleUpstreamWS))
		// Also accept the split shapes on a dedicated upstream port.
		mux.HandleFunc("GET "+strings.TrimRight(path, "/")+"/api", handler(d.handleUpstreamWS))
	case config.KindDownstreamListen:
		if spec.BotID == 0 && spec.BotName == "" {
			return fmt.Errorf("下游独立监听 %s 必须指定 Bot", spec.Name)
		}
		mux.HandleFunc("GET "+path, handler(d.handleDownstreamWS))
		mux.HandleFunc("GET "+path+"/{rest...}", handler(d.handleDownstreamWS))
	default:
		return fmt.Errorf("未知的监听类型 %q", spec.Kind)
	}
	return nil
}

// bindingOf returns the dedicated-listener constraint, if any.
func bindingOf(ctx context.Context) (listenerBinding, bool) {
	binding, ok := ctx.Value(listenerBindingKey{}).(listenerBinding)
	return binding, ok
}

// listenerAuthResult describes why a dedicated listener accepted or refused a
// connection, so the handler can answer 401 (no usable credential) separately
// from 403 (correct credential, wrong account).
type listenerAuthResult int

const (
	listenerAuthOK listenerAuthResult = iota
	listenerAuthNoCredential
	listenerAuthForeignAccount
)

// authorizeUpstreamListener checks a connection against a dedicated listener's
// pinned account.
func (d *DataPlane) authorizeUpstreamListener(binding listenerBinding, token, boundSelfID, selfID string) listenerAuthResult {
	if token == "" {
		return listenerAuthNoCredential
	}
	credentialMatches := false
	if boundSelfID != "" {
		// A per-account token: it must belong to the pinned account.
		credentialMatches = boundSelfID == binding.AccountHint
	} else if d.isBootstrapToken(token) {
		// The bootstrap token is account-agnostic, so the self_id decides.
		credentialMatches = true
	}
	if !credentialMatches {
		return listenerAuthNoCredential
	}
	if selfID != "" && selfID != binding.AccountHint {
		return listenerAuthForeignAccount
	}
	return listenerAuthOK
}

// downstreamListenerAllows reports whether a Bot may use a dedicated downstream
// listener (it must be the Bot the listener was created for).
func (d *DataPlane) downstreamListenerAllows(ctx context.Context, binding listenerBinding, token string) bool {
	if token == "" {
		return false
	}
	if binding.BotID == 0 && binding.BotName == "" {
		return true
	}
	bot, err := d.store.BotByTokenHash(ctx, auth.HashToken(token))
	if err != nil {
		return false
	}
	if binding.BotID != 0 {
		return bot.ID == binding.BotID
	}
	return bot.Name == binding.BotName
}
