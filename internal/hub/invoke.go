package hub

import (
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/onebot"
)

// Invoke errors.
var (
	ErrAccountOffline = errors.New("account offline")
	ErrInvokeBusy     = errors.New("too many in-flight actions")
	ErrInvokeClosed   = errors.New("hub closed")
)

type localPeer struct {
	id   string
	ch   chan []byte
	done chan struct{}
}

func (p *localPeer) ID() string            { return p.id }
func (p *localPeer) Role() string          { return "" }
func (p *localPeer) Done() <-chan struct{} { return p.done }
func (p *localPeer) Close()                {}
func (p *localPeer) Send(raw []byte) bool {
	select {
	case p.ch <- raw:
		return true
	case <-p.done:
		return false
	}
}

var localSeq uint64

// Invoke issues one action against an account on behalf of the management
// console (bypassing bot binding checks) and returns the raw response frame.
func (h *Hub) Invoke(selfID, action string, params json.RawMessage) ([]byte, error) {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	peer := &localPeer{
		id:   "local-" + itoa(atomic.AddUint64(&localSeq, 1)),
		ch:   make(chan []byte, 4),
		done: make(chan struct{}),
	}
	frame := onebot.ActionFrame{Action: action, Params: params}
	if h.tryLocal(0, peer, selfID, frame) {
		select {
		case resp := <-peer.ch:
			return resp, nil
		default:
		}
	}
	api := h.apiPeer(selfID)
	if api == nil {
		return nil, ErrAccountOffline
	}
	key := h.nextKey(peer.ID())
	if !h.setInflight(key, peer, selfID, nil, false) {
		return nil, ErrInvokeBusy
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		h.clearInflight(key)
		return nil, err
	}
	rewritten, err := rewriteEcho(raw, key)
	if err != nil {
		h.clearInflight(key)
		return nil, err
	}
	if !api.Send(rewritten) {
		h.clearInflight(key)
		return nil, ErrInvokeBusy
	}
	select {
	case resp := <-peer.ch:
		return resp, nil
	case <-h.quit:
		return nil, ErrInvokeClosed
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
