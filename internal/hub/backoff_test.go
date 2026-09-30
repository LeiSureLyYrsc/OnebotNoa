package hub

import (
	"testing"
	"time"
)

// fixedRand makes the jitter deterministic: 0.5 means "no jitter applied".
func fixedRand(values ...float64) func() float64 {
	i := 0
	return func() float64 {
		if i >= len(values) {
			return 0.5
		}
		v := values[i]
		i++
		return v
	}
}

func TestBackoffGrowsExponentiallyAndCaps(t *testing.T) {
	min, max := time.Second, 8*time.Second
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 8 * time.Second},  // capped
		{50, 8 * time.Second}, // still capped
	}
	for _, tc := range cases {
		if got := Backoff(tc.attempt, min, max, 0, nil); got != tc.want {
			t.Fatalf("Backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
	// A zero attempt behaves like the first one, and a missing minimum falls
	// back to one second.
	if got := Backoff(0, min, max, 0, nil); got != min {
		t.Fatalf("Backoff(0) = %v, want %v", got, min)
	}
	if got := Backoff(1, 0, max, 0, nil); got != time.Second {
		t.Fatalf("Backoff with zero minimum = %v, want 1s", got)
	}
	if got := Backoff(3, 5*time.Second, time.Second, 0, nil); got != 5*time.Second {
		t.Fatalf("max below min should clamp to min, got %v", got)
	}
}

func TestBackoffJitterStaysInBand(t *testing.T) {
	min, max := 2*time.Second, 30*time.Second

	// Lowest jitter never goes below half the floor.
	low := Backoff(1, min, max, 0.3, fixedRand(0))
	if low < min/2 || low > min {
		t.Fatalf("low jitter delay %v outside [%v, %v]", low, min/2, min)
	}
	// Highest jitter never exceeds 2x the ceiling.
	high := Backoff(10, min, max, 0.3, fixedRand(1))
	if high <= max || high > 2*max {
		t.Fatalf("high jitter delay %v outside (%v, %v]", high, max, 2*max)
	}
	// Jitter 0 is exact.
	if got := Backoff(3, min, max, 0, fixedRand(1)); got != 8*time.Second {
		t.Fatalf("no-jitter delay = %v, want 8s", got)
	}
}

func TestBackoffIsBoundedForHugeAttempts(t *testing.T) {
	for attempt := 1; attempt < 200; attempt++ {
		got := Backoff(attempt, time.Second, time.Minute, 0.5, nil)
		if got <= 0 {
			t.Fatalf("attempt %d produced %v", attempt, got)
		}
		if got > 2*time.Minute {
			t.Fatalf("attempt %d produced %v, above the ceiling", attempt, got)
		}
	}
}
