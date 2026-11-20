// Package retry decides how long to wait before a failed send is tried again (D8).
package retry

import (
	"math/rand/v2"
	"time"
)

// Policy is exponential backoff with equal jitter: the wait doubles with each attempt, from
// Base up to Max, and is then picked at random between half of it and all of it, so
// notifications that failed together don't all come back at the same moment.
type Policy struct {
	Base time.Duration // wait after the first failed attempt, before jitter
	Max  time.Duration // no wait is longer than this
	rand func() float64
}

// New returns a Policy starting at base and capped at maxDelay. base must not exceed maxDelay
// (config.Load checks it).
func New(base, maxDelay time.Duration) Policy {
	return Policy{Base: base, Max: maxDelay, rand: rand.Float64} //nolint:gosec // jitter, not security
}

// Delay returns the wait after failed attempt number attempt (1 for the first). If the
// provider asked for a longer wait (retryAfter), that wins, but no wait exceeds Max.
func (p Policy) Delay(attempt int, retryAfter time.Duration) time.Duration {
	d := p.Base
	for i := 1; i < attempt && d < p.Max; i++ {
		d *= 2
	}
	d = min(d, p.Max)
	half := d / 2
	d = half + time.Duration(p.rand()*float64(d-half))
	return min(max(d, retryAfter), p.Max)
}
