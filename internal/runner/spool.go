package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The spool lets a step ride through a server or database outage. While the
// server cannot be reached (no answer, or 5xx, which includes 503), the
// runner goes on executing the step. Log lines and the final result are kept
// on the runner's own disk, under DataDir/spool/<lease>/, and delivered in
// order when the server answers again. Delivery uses the log positions and
// idempotent finish of the server (see engine/idempotent.go), so a call whose
// answer was lost can be repeated safely. A runner that restarts during an
// outage finds the spool and delivers it.
//
//	logs.jsonl   one log line per line, oldest first, from state.Off on
//	state.json   which lines the server has (Acked), where undelivered ones
//	             start (Off), when the outage began (Since)
//	result.json  the step's result, once it is known
//
// Bounds: log lines are kept up to SpoolMaxBytes and SpoolMaxAge; beyond
// that the oldest are dropped and one line says how many. The result is
// never dropped. Nothing is written to disk until the first failure.
const (
	// DefaultSpoolBytes and DefaultSpoolAge bound the log lines kept per step.
	DefaultSpoolBytes = 256 << 20
	DefaultSpoolAge   = 15 * time.Minute
	spoolBatchBytes   = 1 << 20
)

type spoolState struct {
	Lease   string `json:"lease"`
	Run     string `json:"run,omitempty"`
	Step    string `json:"step,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Since   int64  `json:"since,omitempty"` // ms: the server could not be reached since
	Acked   int    `json:"acked"`           // lines of the step attempt the server has
	Off     int64  `json:"off"`             // offset in logs.jsonl of the first line not delivered
	Dropped int    `json:"dropped,omitempty"`
}

// stepSpool delivers the log lines of one step attempt, keeping them on disk
// while the server cannot be reached.
type stepSpool struct {
	mu       sync.Mutex
	c        *Client
	dir      string
	st       spoolState
	maxBytes int64
	maxAge   time.Duration
	now      func() time.Time
	nextTry  time.Time // after an outage, when to try the server again
}

func (r *Runner) spoolRoot() string { return filepath.Join(r.DataDir, "spool") }

func (r *Runner) newSpool(job *engine.Job) *stepSpool {
	return &stepSpool{c: r.Client, dir: filepath.Join(r.spoolRoot(), safeName(job.LeaseID)),
		st:       spoolState{Lease: job.LeaseID, Run: job.RunID, Step: job.StepID, Attempt: job.Attempt},
		maxBytes: orDefault(r.SpoolMaxBytes, DefaultSpoolBytes), maxAge: orDefault(r.SpoolMaxAge, DefaultSpoolAge), now: time.Now}
}

func orDefault[T int64 | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// safeName keeps a lease id usable as a directory name.
func safeName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			b[i] = '_'
		}
	}
	return string(b)
}

func (s *stepSpool) path(name string) string { return filepath.Join(s.dir, name) }

// spooling reports whether lines are waiting on disk or an outage is on.
func (s *stepSpool) spooling() bool { return s.st.Since != 0 || s.pending() }

func (s *stepSpool) pending() bool {
	fi, err := os.Stat(s.path("logs.jsonl"))
	return err == nil && fi.Size() > s.st.Off
}

// Send delivers a batch of lines, or keeps it for later. It fails only if the
// server no longer knows the lease (ErrLeaseLost) or refuses the lines for a
// reason a later try would not change.
func (s *stepSpool) Send(ctx context.Context, batch []engine.LogLine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(batch) == 0 && !s.spooling() {
		return nil
	}
	if s.spooling() {
		if err := s.append(batch); err != nil {
			return err
		}
		return s.drainLocked(ctx)
	}
	err := s.c.LogsAt(ctx, s.st.Lease, s.st.Acked, batch)
	var gap *LogGap
	if errors.As(err, &gap) {
		s.st.Acked = gap.Have
		err = s.c.LogsAt(ctx, s.st.Lease, s.st.Acked, batch)
	}
	switch {
	case err == nil:
		s.st.Acked += len(batch)
		return nil
	case isOutage(err):
		s.st.Since = s.now().UnixMilli()
		s.nextTry = s.now().Add(time.Second)
		return s.append(batch)
	}
	return err
}

// Drain delivers what is kept, in order. It returns nil when nothing is left
// or the server still cannot be reached (call again later).
func (s *stepSpool) Drain(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drainLocked(ctx)
}

// Done reports whether every kept line has been delivered.
func (s *stepSpool) Done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.spooling()
}

// markOutage records that the server could not be reached, if it is not
// already recorded.
func (s *stepSpool) markOutage() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Since == 0 {
		s.st.Since = s.now().UnixMilli()
	}
}

func (s *stepSpool) drainLocked(ctx context.Context) error {
	if !s.spooling() || s.now().Before(s.nextTry) {
		return nil
	}
	for tries := 0; ; {
		lines, next, err := s.readBatch()
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			break
		}
		err = s.c.LogsAt(ctx, s.st.Lease, s.st.Acked, lines)
		var gap *LogGap
		switch {
		case err == nil:
			s.st.Acked += len(lines)
			s.st.Off = next
			if err := s.save(); err != nil {
				return err
			}
		case errors.As(err, &gap) && tries < 3:
			s.st.Acked = gap.Have
			tries++
		case isOutage(err):
			s.nextTry = s.now().Add(time.Second)
			return nil
		default:
			return err
		}
	}
	if s.st.Since != 0 {
		line := engine.LogLine{At: s.now().UnixMilli(), Stream: "system", Text: gapLine(time.UnixMilli(s.st.Since), s.now())}
		if err := s.c.LogsAt(ctx, s.st.Lease, s.st.Acked, []engine.LogLine{line}); err != nil {
			if isOutage(err) {
				s.nextTry = s.now().Add(time.Second)
				return nil
			}
			return err
		}
		s.st.Acked++
	}
	s.st.Since, s.st.Off = 0, 0
	if _, err := os.Stat(s.path("result.json")); err != nil {
		_ = os.RemoveAll(s.dir) // nothing left to deliver
		return nil
	}
	_ = os.Remove(s.path("logs.jsonl"))
	return s.save()
}

// gapLine is the line the step's log shows for a time the server could not be
// reached.
func gapLine(from, to time.Time) string {
	return fmt.Sprintf("The server could not be reached from %s to %s; nothing was lost.", from.Format("15:04:05"), to.Format("15:04:05"))
}

// append keeps lines on disk and applies the bounds.
func (s *stepSpool) append(batch []engine.LogLine) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range batch {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(s.path("logs.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(buf.Bytes())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := s.trim(); err != nil {
		return err
	}
	return s.save()
}

// readBatch reads undelivered lines from the file, up to what one call may
// carry, and returns where the next batch starts.
func (s *stepSpool) readBatch() ([]engine.LogLine, int64, error) {
	f, err := os.Open(s.path("logs.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, s.st.Off, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(s.st.Off, io.SeekStart); err != nil {
		return nil, 0, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	var out []engine.LogLine
	off, size := s.st.Off, 0
	for len(out) < engine.MaxLogBatch && size < spoolBatchBytes {
		line, err := br.ReadBytes('\n')
		if err != nil && (err != io.EOF || len(line) == 0 || line[len(line)-1] != '\n') {
			break // the end, or a half-written line a crash left
		}
		var l engine.LogLine
		if json.Unmarshal(line, &l) == nil {
			out = append(out, l)
		}
		off += int64(len(line))
		size += len(line)
	}
	return out, off, nil
}

// trim drops the oldest undelivered lines when the file is over its size or
// age bound, and puts one line in their place that says how many.
func (s *stepSpool) trim() error {
	fi, err := os.Stat(s.path("logs.jsonl"))
	if err != nil || fi.Size()-s.st.Off <= 0 {
		return err
	}
	over := fi.Size()-s.st.Off > s.maxBytes
	cut := s.now().Add(-s.maxAge).UnixMilli()
	if !over && !s.firstOlderThan(cut) {
		return nil
	}
	lines, _, err := s.readAll()
	if err != nil {
		return err
	}
	keep := len(lines)
	size := int64(0)
	for i := len(lines) - 1; i >= 0; i-- {
		n := int64(len(lines[i].Text)) + 80
		if size+n > s.maxBytes*9/10 || (lines[i].At != 0 && lines[i].At < cut) {
			keep = len(lines) - 1 - i
			break
		}
		size += n
	}
	dropped := len(lines) - keep
	if dropped <= 0 {
		return nil
	}
	s.st.Dropped += dropped
	marker := engine.LogLine{At: lines[dropped].At, Stream: "system",
		Text: fmt.Sprintf("%d log lines were dropped here because the server could not be reached for long and the runner's spool was full; the step's result is kept.", dropped)}
	out := append([]engine.LogLine{marker}, lines[dropped:]...)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range out {
		_ = enc.Encode(l)
	}
	tmp := s.path("logs.jsonl.tmp")
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	s.st.Off = 0
	return os.Rename(tmp, s.path("logs.jsonl"))
}

func (s *stepSpool) firstOlderThan(cut int64) bool {
	lines, _, err := s.readBatchN(1)
	return err == nil && len(lines) == 1 && lines[0].At != 0 && lines[0].At < cut
}

func (s *stepSpool) readBatchN(n int) ([]engine.LogLine, int64, error) {
	all, off, err := s.readAll()
	if len(all) > n {
		all = all[:n]
	}
	return all, off, err
}

func (s *stepSpool) readAll() ([]engine.LogLine, int64, error) {
	data, err := os.ReadFile(s.path("logs.jsonl"))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) < s.st.Off {
		return nil, 0, nil
	}
	var out []engine.LogLine
	for _, ln := range bytes.Split(data[s.st.Off:], []byte{'\n'}) {
		var l engine.LogLine
		if len(ln) > 0 && json.Unmarshal(ln, &l) == nil {
			out = append(out, l)
		}
	}
	return out, int64(len(data)), nil
}

// save writes the state atomically.
func (s *stepSpool) save() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(s.st)
	tmp := s.path("state.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path("state.json"))
}

// SaveResult keeps the step's result on disk until the server takes it.
func (s *stepSpool) SaveResult(res engine.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if err := s.save(); err != nil {
		return err
	}
	tmp := s.path("result.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path("result.json"))
}

func (s *stepSpool) loadResult() (*engine.Result, error) {
	data, err := os.ReadFile(s.path("result.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var res engine.Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (s *stepSpool) remove() { _ = os.RemoveAll(s.dir) }

// loadSpool reads a spool directory left by this or an earlier process.
func (r *Runner) loadSpool(dir string) (*stepSpool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	s := &stepSpool{c: r.Client, dir: dir, maxBytes: orDefault(r.SpoolMaxBytes, DefaultSpoolBytes),
		maxAge: orDefault(r.SpoolMaxAge, DefaultSpoolAge), now: time.Now}
	if err := json.Unmarshal(data, &s.st); err != nil {
		return nil, err
	}
	return s, nil
}

// isOutage reports whether err means the server cannot be reached or cannot
// serve for now (no answer, a 5xx, a timeout), as against an answer that a
// later try would not change.
func isOutage(err error) bool {
	if err == nil || errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrTooLarge) || errors.Is(err, context.Canceled) {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		return he.code >= 500 || he.code == 408 || he.code == 429
	}
	var gap *LogGap
	return !errors.As(err, &gap)
}

// ---- delivery ----

// activeSteps are the leases whose steps this process is still running; their
// spools are theirs to deliver.
type activeSteps struct{ m sync.Map }

func (a *activeSteps) add(lease string)  { a.m.Store(lease, struct{}{}) }
func (a *activeSteps) done(lease string) { a.m.Delete(lease) }
func (a *activeSteps) has(lease string) bool {
	_, ok := a.m.Load(lease)
	return ok
}

// deliverSpools runs for the life of the runner: it delivers the results and
// logs of steps that ended during an outage, and what an earlier process left.
func (r *Runner) deliverSpools(ctx context.Context) {
	delay := time.Second
	for {
		if r.deliverSpoolsOnce(ctx) {
			delay = time.Second
		} else {
			delay = min(delay*2, 5*time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-r.spoolKick:
		case <-time.After(delay):
		}
	}
}

// deliverSpoolsOnce tries every spool once; it reports whether none was held
// back by an outage.
func (r *Runner) deliverSpoolsOnce(ctx context.Context) bool {
	entries, _ := os.ReadDir(r.spoolRoot())
	ok := true
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := r.loadSpool(filepath.Join(r.spoolRoot(), e.Name()))
		if err != nil {
			if fi, ierr := e.Info(); ierr == nil && time.Since(fi.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(filepath.Join(r.spoolRoot(), e.Name())) // nothing readable, and old
			}
			continue
		}
		if r.spoolActive.has(s.st.Lease) {
			continue
		}
		if !r.deliverSpool(ctx, s) {
			ok = false
		}
	}
	return ok
}

// deliverSpool delivers one finished (or orphaned) step's lines and result.
func (r *Runner) deliverSpool(ctx context.Context, s *stepSpool) bool {
	log := r.Log.With("run", s.st.Run, "step", s.st.Step, "attempt", s.st.Attempt)
	// The lease is kept alive while its result waits, as the step's own
	// heartbeats did; an answer of "gone" shows up in the calls below.
	_, _ = r.Client.Heartbeat(ctx, s.st.Lease)
	if err := s.Drain(ctx); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			log.Warn("the server no longer has this step's lease; dropping what was spooled")
			s.remove()
			return true
		}
		log.Warn("delivering spooled log lines failed; dropping them", "err", err)
		_ = os.Remove(s.path("logs.jsonl"))
		return true
	}
	if !s.Done() {
		return false // still unreachable
	}
	res, err := s.loadResult()
	if err != nil {
		log.Warn("the spooled result cannot be read; dropping it", "err", err)
		s.remove()
		return true
	}
	if res == nil {
		// Left by a process that died with the step: its lease runs out and
		// the server gives the step to someone else.
		s.remove()
		return true
	}
	err = r.reportResult(ctx, log, s.st.Lease, res)
	switch {
	case err == nil || errors.Is(err, ErrLeaseLost):
		r.finished(log, *res)
		s.remove()
		log.Info("delivered a result that was kept while the server could not be reached")
		return true
	case isOutage(err):
		return false
	}
	log.Warn("the server refused the spooled result; dropping it", "err", err)
	s.remove()
	return true
}
