package localauth

import (
	"sync"
	"time"
)

// Backoff says how long an account stays locked after this many failed
// sign-ins in a row. The first four failures cost nothing; the fifth locks
// the account for 30 seconds, and each further failure doubles the lock up
// to 15 minutes. A success clears the count. The lock is temporary on
// purpose: a permanent lock lets anyone lock an administrator out.
func Backoff(failures int) time.Duration {
	const free = 4
	if failures <= free {
		return 0
	}
	d := 30 * time.Second
	for i := free + 1; i < failures && d < 15*time.Minute; i++ {
		d *= 2
	}
	return min(d, 15*time.Minute)
}

// Limiter bounds attempts per key (a client address) in memory: at most
// Max attempts in any Window. It protects the cost of a hash against
// someone trying many accounts from one place; the per-account lock in the
// database protects each account against someone trying many places.
type Limiter struct {
	Max    int
	Window time.Duration
	mu     sync.Mutex
	hits   map[string][]time.Time
}

// Allow records an attempt and reports whether it is within the limit and, if
// not, how long to wait.
func (l *Limiter) Allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	cut := now.Add(-l.Window)
	h := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			h = append(h, t)
		}
	}
	if len(h) >= l.Max {
		l.hits[key] = h
		return false, h[0].Add(l.Window).Sub(now)
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 10000 { // forget idle keys
		for k, v := range l.hits {
			if len(v) == 0 || !v[len(v)-1].After(cut) {
				delete(l.hits, k)
			}
		}
	}
	return true, 0
}
