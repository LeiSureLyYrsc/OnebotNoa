package hub

import (
	"testing"
	"time"
)

func drain(q *sendQueue) [][]byte {
	out := [][]byte{}
	for {
		select {
		case frame := <-q.ch:
			out = append(out, frame)
		default:
			return out
		}
	}
}

func TestSendQueueDropOldest(t *testing.T) {
	q := newSendQueue(2, PolicyDropOldest, time.Second, nil)
	for _, frame := range []string{"a", "b", "c"} {
		if !q.Push([]byte(frame)) {
			t.Fatalf("drop_oldest must always accept, %s was refused", frame)
		}
	}
	got := drain(q)
	if len(got) != 2 {
		t.Fatalf("queue holds %d frames, want 2", len(got))
	}
	// The oldest frame was evicted, the newest kept.
	if string(got[0]) != "b" || string(got[1]) != "c" {
		t.Fatalf("kept %q and %q, want b and c", got[0], got[1])
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", q.Dropped())
	}
}

func TestSendQueueDropNewest(t *testing.T) {
	q := newSendQueue(2, PolicyDropNewest, time.Second, nil)
	if !q.Push([]byte("a")) || !q.Push([]byte("b")) {
		t.Fatal("the first two frames fit")
	}
	if q.Push([]byte("c")) {
		t.Fatal("the third frame must be refused")
	}
	got := drain(q)
	if len(got) != 2 || string(got[0]) != "a" || string(got[1]) != "b" {
		t.Fatalf("queue = %q", got)
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", q.Dropped())
	}
}

func TestSendQueueDisconnectTriggersCallback(t *testing.T) {
	triggered := make(chan struct{}, 1)
	q := newSendQueue(1, PolicyDisconnect, time.Second, func() { triggered <- struct{}{} })
	if !q.Push([]byte("a")) {
		t.Fatal("first frame fits")
	}
	if q.Push([]byte("b")) {
		t.Fatal("second frame must be refused")
	}
	select {
	case <-triggered:
	case <-time.After(time.Second):
		t.Fatal("overflow callback was not called")
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", q.Dropped())
	}
}

func TestSendQueueBlockTimesOut(t *testing.T) {
	q := newSendQueue(1, PolicyBlock, 50*time.Millisecond, nil)
	if !q.Push([]byte("a")) {
		t.Fatal("first frame fits")
	}
	start := time.Now()
	if q.Push([]byte("b")) {
		t.Fatal("a full queue with block policy must eventually refuse")
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("block policy returned after %v, expected to wait", elapsed)
	}
	// After the reader drains, the push succeeds again.
	<-q.ch
	if !q.Push([]byte("c")) {
		t.Fatal("space is available again")
	}
}

func TestSendQueueCloseRefusesFrames(t *testing.T) {
	q := newSendQueue(2, PolicyDropOldest, time.Second, nil)
	q.Close()
	if q.Push([]byte("a")) {
		t.Fatal("a closed queue must refuse frames")
	}
	// Closing twice must not panic, and the channel must stay usable for a
	// producer that was already inside Push (it is never closed).
	q.Close()
	q.ch <- []byte("late")
	if q.Len() != 1 {
		t.Fatalf("queue length = %d, want 1", q.Len())
	}
}
