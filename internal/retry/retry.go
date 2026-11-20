// Package retry decides how long to wait before a failed send is tried again (D8).
package retry

import (
	"math/rand/v2"
	"time"
)

// Policy allows up to MaxAttempts attempts, with exponential backoff and equal jitter between
// them: the wait doubles with each attempt, from Base up to Max, and is then picked at random
// between half of it and all of it, so notifications that failed together don't all come
// back at the same moment.
type Policy struct {
	MaxAttempts int           // attempts in total, the first one included
	Base        time.Duration // wait after the first failed attempt, before jitter
	Max         time.Duration // no wait is longer than this
	rand        func() float64
}

// New returns a Policy of maxAttempts attempts, waits starting at base and capped at maxDelay.
// The values are expected to be valid (config.Load checks them).
func New(maxAttempts int, base, maxDelay time.Duration) Policy {
	return Policy{MaxAttempts: maxAttempts, Base: base, Max: maxDelay, rand: rand.Float64} //nolint:gosec // jitter, not security
}

// Next says what to do after failed attempt number attempt (1 for the first): wait that long
// and try again, or, with ok false, stop because no attempts are left.
func (p Policy) Next(attempt int, retryAfter time.Duration) (wait time.Duration, ok bool) {
	if attempt >= p.MaxAttempts {
		return 0, false
	}
	return p.Delay(attempt, retryAfter), true
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
