package loadtest

import (
	"slices"
	"testing"
	"time"
)

// every returns n times, gap apart, starting at start.
func every(start time.Time, gap time.Duration, n int) []time.Time {
	ts := make([]time.Time, n)
	for i := range ts {
		ts[i] = start.Add(time.Duration(i) * gap)
	}
	return ts
}

func TestMeasure(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		times     []time.Time
		busiest   int
		rate      float64
		perSecond []int
	}{
		{
			name:      "steady 100 per second",
			times:     every(t0, 10*time.Millisecond, 250),
			busiest:   100,
			rate:      100,
			perSecond: []int{100, 100, 50},
		},
		{
			name:      "burst of 10 at once, then 100 per second",
			times:     append(every(t0, 0, 9), every(t0, 10*time.Millisecond, 200)...),
			busiest:   109, // the 9 extra at t0 plus 100 in the first second
			rate:      208.0 / 1.99,
			perSecond: []int{109, 100},
		},
		{
			name:      "a send exactly one second later is in the next window",
			times:     []time.Time{t0, t0.Add(time.Second)},
			busiest:   1,
			rate:      1,
			perSecond: []int{1, 1},
		},
		{
			name:      "order doesn't matter",
			times:     []time.Time{t0.Add(500 * time.Millisecond), t0, t0.Add(900 * time.Millisecond)},
			busiest:   3,
			rate:      2 / 0.9,
			perSecond: []int{3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Measure(tt.times)
			if r.Sent != len(tt.times) || r.Busiest != tt.busiest || !slices.Equal(r.PerSecond, tt.perSecond) {
				t.Errorf("sent %d, busiest %d, per second %v; want %d, %d, %v",
					r.Sent, r.Busiest, r.PerSecond, len(tt.times), tt.busiest, tt.perSecond)
			}
			if diff := r.Rate - tt.rate; diff > 0.01 || diff < -0.01 {
				t.Errorf("rate = %.2f, want %.2f", r.Rate, tt.rate)
			}
		})
	}
}

func TestMeasure_NoSends(t *testing.T) {
	if r := Measure(nil); r.Sent != 0 || r.Busiest != 0 || r.PerSecond != nil {
		t.Errorf("Measure(nil) = %+v, want zero", r)
	}
}
