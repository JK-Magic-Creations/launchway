package storage

import (
	"context"
	"errors"
	"fmt"
)

// The named points of a push where a failure can be injected. The names are
// the ones the storage spike used.
const (
	PtPreReceive     = "pre-receive:start"
	PtAfterUpload    = "pre-receive:after-upload"
	PtAfterRegister  = "pre-receive:after-register"
	PtPrepared       = "prepared:start"
	PtTxAfterCheck   = "tx:after-check"
	PtTxAfterRefs    = "tx:after-refs"
	PtTxAfterPack    = "tx:after-pack-live"
	PtTxBeforeCommit = "tx:before-commit"
	PtTxAfterCommit  = "tx:after-commit"
	PtCommitted      = "committed:start"
	PtPostReceive    = "post-receive:start"
)

// PushPoints is every point of a push, in order.
var PushPoints = []string{PtPreReceive, PtAfterUpload, PtAfterRegister, PtPrepared, PtTxAfterCheck, PtTxAfterRefs,
	PtTxAfterPack, PtTxBeforeCommit, PtTxAfterCommit, PtCommitted, PtPostReceive}

// ErrInjected is the error an injected failure returns.
var ErrInjected = errors.New("injected failure")

// Faults lets tests make the push path fail at a named point. Production code
// passes none. A fault may return an error, or end the process.
type Faults interface {
	// Hit is called at each named point; a non-nil error fails the push there.
	Hit(point string) error
	// Drop reports a lost database connection: when is "before" the commit
	// statement or "after" it (the commit happened, the answer was lost).
	Drop(when string) error
}

type faultsKey struct{}

// WithFaults attaches faults to a context. The push path reads them from there,
// so one request can be attacked without touching the others.
func WithFaults(ctx context.Context, f Faults) context.Context {
	return context.WithValue(ctx, faultsKey{}, f)
}

func faultsFrom(ctx context.Context) Faults {
	if f, ok := ctx.Value(faultsKey{}).(Faults); ok {
		return f
	}
	return nil
}

// A nil Faults is no faults; these helpers make a nil value safe to call.
type noFaults struct{}

func (noFaults) Hit(string) error  { return nil }
func (noFaults) Drop(string) error { return nil }

func orNone(f Faults) Faults {
	if f == nil {
		return noFaults{}
	}
	return f
}

func injected(point string) error { return fmt.Errorf("%w at %s", ErrInjected, point) }
