package hub

import (
	"sync/atomic"
	"time"
)

// Backpressure policies for a connection's outbound queue.
const (
	PolicyDropOldest = "drop_oldest"
	PolicyDropNewest = "drop_newest"
	PolicyDisconnect = "disconnect"
	PolicyBlock      = "block"
)

// sendQueue is a bounded outbound queue with an explicit backpressure policy.
// It is deliberately socket-free so the policies can be unit tested.
type sendQueue struct {
	ch       chan []byte
	policy   string
	blockFor time.Duration
	dropped  atomic.Int64
	closed   atomic.Bool

	// onOverflow runs when the policy is "disconnect" and the queue is full.
	onOverflow func()
}

func newSendQueue(size int, policy string, blockFor time.Duration, onOverflow func()) *sendQueue {
	if size <= 0 {
		size = 1024
	}
	switch policy {
	case PolicyDropOldest, PolicyDropNewest, PolicyDisconnect, PolicyBlock:
	default:
		policy = PolicyDropOldest
	}
	if blockFor <= 0 {
		blockFor = 5 * time.Second
	}
	return &sendQueue{ch: make(chan []byte, size), policy: policy, blockFor: blockFor, onOverflow: onOverflow}
}

// Push enqueues a frame, returning false when the frame was dropped.
func (q *sendQueue) Push(raw []byte) bool {
	if q.closed.Load() {
		return false
	}
	switch q.policy {
	case PolicyDropNewest:
		select {
		case q.ch <- raw:
			return true
		default:
			q.dropped.Add(1)
			return false
		}
	case PolicyDisconnect:
		select {
		case q.ch <- raw:
			return true
		default:
			q.dropped.Add(1)
			if q.onOverflow != nil {
				q.onOverflow()
			}
			return false
		}
	case PolicyBlock:
		timer := time.NewTimer(q.blockFor)
		defer timer.Stop()
		select {
		case q.ch <- raw:
			return true
		case <-timer.C:
			q.dropped.Add(1)
			return false
		}
	default: // drop_oldest
		select {
		case q.ch <- raw:
			return true
		default:
		}
		select {
		case <-q.ch:
			q.dropped.Add(1)
		default:
		}
		select {
		case q.ch <- raw:
			return true
		default:
			q.dropped.Add(1)
			return false
		}
	}
}

// C exposes the frame channel for select loops (the write loop needs to wake on
// pings and closure as well). Callers must not send on it.
func (q *sendQueue) C() <-chan []byte { return q.ch }

// Close stops accepting frames.
//
// The channel is deliberately NOT closed: a producer may be in the middle of a
// send when the consumer goes away, and closing it would turn that into a
// "send on closed channel" panic (the race detector catches the same thing).
// Readers stop through the connection's done channel instead, so the queue can
// simply be abandoned.
func (q *sendQueue) Close() {
	q.closed.CompareAndSwap(false, true)
}

// Len reports the number of queued frames.
func (q *sendQueue) Len() int { return len(q.ch) }

// Dropped reports how many frames the policy discarded.
func (q *sendQueue) Dropped() int64 { return q.dropped.Load() }

// Policy reports the active policy name.
func (q *sendQueue) Policy() string { return q.policy }
