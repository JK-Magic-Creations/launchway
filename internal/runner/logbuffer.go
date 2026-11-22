package runner

import (
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// logBuffer batches lines for the next flush. Its size is bounded: when the
// server cannot keep up with a step that prints very fast, the step waits a
// little at the pipe (backpressure) instead of the runner holding without
// limit what it has not delivered. The wait is short and then the line is
// kept anyway, so a step is slowed, never stopped, and no line is lost.
type logBuffer struct {
	mu    sync.Mutex
	cond  *sync.Cond
	lines []engine.LogLine
}

const (
	// logBufferMax is how many undelivered lines a step may hold before its
	// output waits for a flush.
	logBufferMax = 50_000
	// logBufferWait is the longest such a wait lasts.
	logBufferWait = 2 * time.Second
	// logFlushEvery is how often a step's waiting lines are sent: a line
	// reaches the server within about this time.
	logFlushEvery = 150 * time.Millisecond
)

func newLogBuffer() *logBuffer {
	b := &logBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *logBuffer) add(stream, text string) {
	b.mu.Lock()
	if len(b.lines) >= logBufferMax {
		// Wait for a flush, but not for long (and not forever if none runs).
		t := time.AfterFunc(logBufferWait, func() { b.mu.Lock(); b.cond.Broadcast(); b.mu.Unlock() })
		start := time.Now()
		for len(b.lines) >= logBufferMax && time.Since(start) < logBufferWait {
			b.cond.Wait()
		}
		t.Stop()
	}
	b.lines = append(b.lines, engine.LogLine{At: time.Now().UnixMilli(), Stream: stream, Text: text})
	b.mu.Unlock()
}

func (b *logBuffer) take(limit int) []engine.LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(b.lines), limit)
	out := append([]engine.LogLine(nil), b.lines[:n]...)
	b.lines = b.lines[n:]
	if len(b.lines) == 0 {
		b.lines = nil
	}
	b.cond.Broadcast()
	return out
}
