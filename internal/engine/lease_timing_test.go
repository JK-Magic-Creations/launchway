package engine

import (
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The lease a server gives by default rides out a server that is slow or
// silent for a minute: it lasts DefaultLeaseTTL, and the runner is told to
// send a heartbeat every 15 s, so five heartbeats in a row must fail before
// the step is taken back. (It was 30 s with a heartbeat every 10 s.)
func TestDefaultLeaseOutlastsSlowHeartbeats(t *testing.T) {
	e, err := Open(Options{Database: testdb.New(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.ttl != 90*time.Second || DefaultLeaseTTL != 90*time.Second {
		t.Fatalf("lease %v", e.ttl)
	}
	if e.beat() != 15*time.Second {
		t.Fatalf("beat %v", e.beat())
	}
	if missed := int(e.ttl/e.beat()) - 1; missed < 5 {
		t.Fatalf("only %d heartbeats may be missed", missed)
	}
	if e.runnerTimeout != 90*time.Second {
		t.Fatalf("runner timeout %v", e.runnerTimeout)
	}
	// Short leases (tests, small installations) keep a third of the lease as the beat.
	e.ttl = 6 * time.Second
	if e.beat() != 2*time.Second {
		t.Fatalf("beat %v", e.beat())
	}
}
