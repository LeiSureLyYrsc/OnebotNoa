package hub

import (
	"math/rand/v2"
	"time"
)

// Backoff computes the delay before the next reconnect attempt: exponential from
// min to max, with a proportional jitter to keep many endpoints from retrying in
// lockstep. randf is injectable so the schedule is testable.
func Backoff(attempt int, min, max time.Duration, jitter float64, randf func() float64) time.Duration {
	if min <= 0 {
		min = time.Second
	}
	if max < min {
		max = min
	}
	if attempt < 1 {
		attempt = 1
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}

	delay := float64(min)
	for i := 1; i < attempt; i++ {
		if delay >= float64(max) {
			break
		}
		delay *= 2
	}
	if delay > float64(max) {
		delay = float64(max)
	}
	if jitter > 0 {
		if randf == nil {
			randf = rand.Float64
		}
		// ±jitter around the computed delay.
		delay *= 1 + jitter*(2*randf()-1)
	}
	// Never wait less than half the floor, and never more than 2x the ceiling.
	floor := float64(min) / 2
	if delay < floor {
		delay = floor
	}
	if ceiling := float64(max) * 2; delay > ceiling {
		delay = ceiling
	}
	if delay < 0 {
		delay = 0
	}
	return time.Duration(delay)
}
