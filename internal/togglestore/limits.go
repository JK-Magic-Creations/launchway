package togglestore

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limits are the SDK routes' memory: a rate limiter per key and kind of call, the streams each key has open and
// the keys an address got wrong lately. It is in memory and per server, which is what a rate limit needs.
type Limits struct {
	mu      sync.Mutex
	limits  map[string]*rate.Limiter
	streams map[string]int
	fails   map[string]failWindow
}

// failWindow counts the keys an address got wrong in the current minute.
type failWindow struct {
	start time.Time
	n     int
}

const maxKeyFailures = 30

// TooManyFailures says whether an address has sent too many keys that were not valid in the last minute.
func (rt *Limits) TooManyFailures(addr string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	f, ok := rt.fails[addr]
	return ok && time.Since(f.start) < time.Minute && f.n >= maxKeyFailures
}

// Failed records a key that was not valid from an address.
func (rt *Limits) Failed(addr string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.fails == nil || len(rt.fails) > 20000 {
		rt.fails = map[string]failWindow{}
	}
	f := rt.fails[addr]
	if time.Since(f.start) >= time.Minute {
		f = failWindow{start: time.Now()}
	}
	f.n++
	rt.fails[addr] = f
}

// MaxStreamsPerKey is how many streams one key may have open.
const MaxStreamsPerKey = 20

// Allow spends one token of a key's bucket for one kind of call.
func (rt *Limits) Allow(id, kind string, perMinute float64, burst int) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.limits == nil || len(rt.limits) > 20000 {
		rt.limits = map[string]*rate.Limiter{}
	}
	k := id + "|" + kind
	l := rt.limits[k]
	if l == nil {
		l = rate.NewLimiter(rate.Limit(perMinute/60), burst)
		rt.limits[k] = l
	}
	return l.Allow()
}

// OpenStream counts a stream as open, or says the key has too many.
func (rt *Limits) OpenStream(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.streams == nil {
		rt.streams = map[string]int{}
	}
	if rt.streams[id] >= MaxStreamsPerKey {
		return false
	}
	rt.streams[id]++
	return true
}

// CloseStream counts it closed.
func (rt *Limits) CloseStream(id string) {
	rt.mu.Lock()
	rt.streams[id]--
	rt.mu.Unlock()
}
