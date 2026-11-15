package jsonscan

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Deadline tells a parser when to stop: the context ends or the time limit
// passes. It only looks at the clock every so often.
type Deadline struct {
	ctx   context.Context
	start time.Time
	at    time.Time
	limit time.Duration
	n     int
}

// NewDeadline starts the clock.
func NewDeadline(ctx context.Context, limit time.Duration) *Deadline {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	return &Deadline{ctx: ctx, start: now, at: now.Add(limit), limit: limit}
}

// Tick is called once per value or element; every 1000 calls it checks the
// clock and the context.
func (d *Deadline) Tick() bool {
	d.n++
	if d.n%1000 != 0 {
		return false
	}
	return d.Expired()
}

// Expired checks the clock and the context now.
func (d *Deadline) Expired() bool {
	return d.ctx.Err() != nil || !time.Now().Before(d.at)
}

// What says why parsing stopped, as words that fit after "parsing".
func (d *Deadline) What() string {
	if errors.Is(d.ctx.Err(), context.Canceled) {
		return "was cancelled"
	}
	if d.ctx.Err() != nil {
		return "stopped after " + time.Since(d.start).Round(time.Millisecond).String()
	}
	return "stopped after " + d.limit.String()
}

// StopError is returned when the time ran out before anything was read.
// what names the things not read, such as "results".
func (d *Deadline) StopError(what string) error {
	if err := d.ctx.Err(); err != nil {
		return fmt.Errorf("parsing %s before any %s were read: %w", d.What(), what, err)
	}
	return fmt.Errorf("parsing %s before any %s were read", d.What(), what)
}
