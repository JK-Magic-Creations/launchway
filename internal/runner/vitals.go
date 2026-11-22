package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A runner that is up but not asking for work looks, from outside, just like
// one that has nothing to do: it logs nothing either way. These vitals make the
// difference visible and let a supervisor act on it:
//
//   - each loop that takes work from the server stamps the time it last got an
//     answer (or went to ask), so that a loop stuck somewhere other than the
//     long poll is seen;
//   - /livez answers 503 when a loop has not polled for StallAfter (not
//     while it waits for a free slot and every action holding one is within its
//     own time);
//   - a watchdog logs an error with the stacks of all goroutines and ends the
//     process when that has lasted, so that it is started again;
//   - one line at a low rate says how many polls were made and when work was
//     last taken, so silence is never ambiguous.

// ErrStalled is what a runner's Run returns when its watchdog ended it.
var ErrStalled = errors.New("a loop that takes work from the server stopped polling")

// defaultStallAfter is how long a loop may go without a poll before the runner
// is judged stuck: well above the longest a healthy loop is away from the
// server (a long poll of 25 s, a failed call of up to a minute, a back-off of
// up to 30 s).
const defaultStallAfter = 5 * time.Minute

// loopVital is the pulse of one loop that takes work.
type loopVital struct {
	name    string
	last    atomic.Int64 // when the loop last started a poll or got its answer (UnixNano)
	waiting atomic.Bool  // blocked until a slot is free
	polls   atomic.Int64
}

func (l *loopVital) beat() {
	if l != nil {
		l.last.Store(time.Now().UnixNano())
	}
}

func (l *loopVital) polled() {
	if l != nil {
		l.polls.Add(1)
		l.beat()
	}
}

func (l *loopVital) slotWait(b bool) {
	if l != nil {
		l.waiting.Store(b)
		l.beat()
	}
}

// actVital is one action in progress: abandonAt is when the runner gives up
// waiting for it (zero: it may take as long as it takes).
type actVital struct {
	what      string
	abandonAt time.Time
}

type vitals struct {
	started    time.Time
	stallAfter time.Duration

	mu    sync.Mutex
	loops []*loopVital
	acts  map[uint64]actVital
	next  uint64

	lastWork  atomic.Int64 // UnixNano
	workWhat  atomic.Value // string
	abandoned atomic.Int64
}

func newVitals(stallAfter time.Duration) *vitals {
	if stallAfter <= 0 {
		stallAfter = defaultStallAfter
	}
	return &vitals{started: time.Now(), stallAfter: stallAfter, acts: map[uint64]actVital{}}
}

// loop registers a loop; a nil vitals gives a loop that records nothing.
func (v *vitals) loop(name string) *loopVital {
	if v == nil {
		return nil
	}
	l := &loopVital{name: name}
	l.beat()
	v.mu.Lock()
	v.loops = append(v.loops, l)
	v.mu.Unlock()
	return l
}

// begin records that an action started; end must be called when it is over.
func (v *vitals) begin(what string, abandonAt time.Time) (end func()) {
	if v == nil {
		return func() {}
	}
	v.lastWork.Store(time.Now().UnixNano())
	v.workWhat.Store(what)
	v.mu.Lock()
	v.next++
	id := v.next
	v.acts[id] = actVital{what: what, abandonAt: abandonAt}
	v.mu.Unlock()
	return func() {
		v.lastWork.Store(time.Now().UnixNano())
		v.mu.Lock()
		delete(v.acts, id)
		v.mu.Unlock()
	}
}

// overdue lists the actions that are still held after the time the runner
// gives them to stop.
func (v *vitals) overdue(now time.Time) []string {
	var out []string
	for _, a := range v.acts {
		if !a.abandonAt.IsZero() && now.After(a.abandonAt.Add(10*time.Second)) {
			out = append(out, a.what)
		}
	}
	slices.Sort(out)
	return out
}

// check says whether every loop has polled recently enough.
func (v *vitals) check(now time.Time) (bool, string) {
	if v == nil {
		return true, ""
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	over := v.overdue(now)
	for _, l := range v.loops {
		age := now.Sub(time.Unix(0, l.last.Load()))
		if age <= v.stallAfter {
			continue
		}
		if l.waiting.Load() && len(over) == 0 {
			continue // every slot is held by work that is still within its time
		}
		why := fmt.Sprintf("the %s loop has not polled the server for %s", l.name, age.Round(time.Second))
		if l.waiting.Load() {
			why += " (it waits for a free slot, and these actions are past their time: " + strings.Join(over, ", ") + ")"
		}
		return false, why
	}
	return true, ""
}

// summary is the line a runner logs now and then.
func (v *vitals) summary(now time.Time) string {
	v.mu.Lock()
	running := len(v.acts)
	var polls int64
	for _, l := range v.loops {
		polls += l.polls.Load()
	}
	v.mu.Unlock()
	state := "idle"
	if running > 0 {
		state = fmt.Sprintf("busy (%d running)", running)
	}
	last := "no work taken yet"
	if ns := v.lastWork.Load(); ns != 0 {
		what, _ := v.workWhat.Load().(string)
		last = fmt.Sprintf("last work %s ago (%s)", now.Sub(time.Unix(0, ns)).Round(time.Second), what)
	}
	s := fmt.Sprintf("%s: %d polls, %s", state, polls, last)
	if n := v.abandoned.Load(); n > 0 {
		s += fmt.Sprintf(", %d actions abandoned after their time", n)
	}
	return s
}

// report logs the summary every so often until ctx ends.
func (v *vitals) report(ctx context.Context, log *slog.Logger, every time.Duration, what string) {
	if v == nil || every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			log.Info(what+" "+v.summary(time.Now()), "up", time.Since(v.started).Round(time.Second))
		}
	}
}

// guard ends the runner when a loop stalls: it logs the stacks of all
// goroutines at error level and sends ErrStalled on the channel.
func (v *vitals) guard(ctx context.Context, log *slog.Logger, stalled chan<- error) {
	if v == nil {
		return
	}
	t := time.NewTicker(max(min(v.stallAfter/4, 30*time.Second), 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ok, why := v.check(time.Now())
		if ok {
			continue
		}
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		log.Error("a loop that takes work has stopped polling; ending the process so that it is started again", "why", why, "goroutines", string(buf))
		stalled <- fmt.Errorf("%w: %s", ErrStalled, why)
		return
	}
}

// liveHandler serves /livez: 200 while every loop polls, 503 and the reason
// when one has stopped.
func (v *vitals) liveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if ok, why := v.check(time.Now()); !ok {
			http.Error(w, why, http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	}
}

// ctxLock is a lock that can be waited for with a context: a step that waits
// for something another step holds gives up when its own time is over, where a
// sync.Mutex would wait for as long as the holder does.
type ctxLock chan struct{}

func newCtxLock() ctxLock { return make(ctxLock, 1) }

func (l ctxLock) lock(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l ctxLock) unlock() { <-l }
