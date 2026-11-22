package engine

import "time"

// DefaultLeaseTTL is how long a step's lease lasts without a heartbeat. The
// runner sends one every Beat (at most 15 s), so a server that is slow or
// unreachable for a minute does not cost a running step its lease. It was
// 30 s with a heartbeat every 10 s, which a server busy for 15 to 30 s could
// break.
const DefaultLeaseTTL = 90 * time.Second

// maxBeat is the longest pause between two heartbeats.
const maxBeat = 15 * time.Second

// beat is how often a runner renews a lease: a third of the lease, but no
// more than maxBeat, so that a long lease means more tries, not fewer.
func (e *Engine) beat() time.Duration { return min(e.ttl/3, maxBeat) }

// defaultLease sets the lease and the runner timeout of options that name neither.
func (o *Options) defaultLease() {
	if o.RunnerTimeout == 0 && o.LeaseTTL == 0 {
		o.RunnerTimeout = 90 * time.Second // as before the lease got longer
	}
	if o.LeaseTTL == 0 {
		o.LeaseTTL = DefaultLeaseTTL
	}
}
