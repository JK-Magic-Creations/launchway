package engine

import (
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// While a server of an older release is still connected (a rolling update on PostgreSQL), a deploy action queued
// now carries no short pickup deadline: the older server would read it as the step's whole time limit and expire a
// step that was taken and is running.
func TestNoPickupDeadlineWhileAnOlderServerRuns(t *testing.T) {
	e, err := Open(Options{Database: testdb.New(t), Queues: QueueSettings{DeployPickupWait: 15 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	c := &cmd{e: e, st: &RunState{}, now: time.Now().UnixMilli()}
	short := c.deployPickupDeadline()
	if short-c.now > int64(time.Hour/time.Millisecond) {
		t.Fatalf("with no older server the deadline is the pickup wait: %d ms", short-c.now)
	}
	olderSeen.Store(e, olderCheck{at: time.Now(), some: true})
	defer olderSeen.Delete(e)
	if long := c.deployPickupDeadline(); long-c.now < 365*24*int64(time.Hour/time.Millisecond) {
		t.Fatalf("an older server runs, and the deadline is %d ms: it would expire a running step", long-c.now)
	}
}
