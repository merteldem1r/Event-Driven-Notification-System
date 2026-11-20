package retry

import (
	"testing"
	"time"
)

// fixed returns a Policy whose jitter always draws r.
func fixed(r float64) Policy {
	p := New(10*time.Second, 10*time.Minute)
	p.rand = func() float64 { return r }
	return p
}

func TestDelay(t *testing.T) {
	tests := []struct {
		name       string
		attempt    int
		retryAfter time.Duration
		rand       float64
		want       time.Duration
	}{
		// Backoff before jitter: 10 s, 20 s, 40 s, 80 s, ... up to 10 min; rand 0 gives half of it.
		{"first failure, longest", 1, 0, 0.9999999999, 10 * time.Second},
		{"first failure, shortest", 1, 0, 0, 5 * time.Second},
		{"first failure, middle", 1, 0, 0.5, 7500 * time.Millisecond},
		{"second failure doubles", 2, 0, 0, 10 * time.Second},
		{"fourth failure", 4, 0, 0, 40 * time.Second},
		{"capped at the maximum", 10, 0, 0, 5 * time.Minute},
		{"capped, longest", 10, 0, 0.9999999999, 10 * time.Minute},
		{"huge attempt number doesn't overflow", 1000, 0, 0, 5 * time.Minute},
		{"longer Retry-After wins", 1, 30 * time.Second, 0, 30 * time.Second},
		{"shorter Retry-After is ignored", 2, 3 * time.Second, 0, 10 * time.Second},
		{"Retry-After is capped too", 1, 48 * time.Hour, 0, 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixed(tt.rand).Delay(tt.attempt, tt.retryAfter); got.Round(time.Millisecond) != tt.want {
				t.Errorf("Delay(%d, %v) = %v, want %v", tt.attempt, tt.retryAfter, got, tt.want)
			}
		})
	}
}

// With the real random source, every wait lies between half and all of the backoff.
func TestDelay_JitterStaysInRange(t *testing.T) {
	p := New(10*time.Second, 10*time.Minute)
	seen := map[time.Duration]bool{}
	for range 1000 {
		d := p.Delay(3, 0) // backoff 40 s
		if d < 20*time.Second || d > 40*time.Second {
			t.Fatalf("Delay = %v, want 20s to 40s", d)
		}
		seen[d.Round(time.Second)] = true
	}
	if len(seen) < 10 {
		t.Errorf("only %d different waits in 1000 draws, want them spread out", len(seen))
	}
}
