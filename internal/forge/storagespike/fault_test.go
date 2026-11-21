package storagespike

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// FaultEnv is the environment variable that carries a fault into the git
// processes of a push: "<point>=<action>[:<arg>]".
const FaultEnv = "LAUNCHWAY_SPIKE_FAULT"

// ErrInjected is the error an "error" fault produces.
var ErrInjected = errors.New("injected failure")

// Fault is one injected failure: at a named point of the push or the sync,
// do something.
type Fault struct {
	Point  string
	Action string // kill, error, sleep, dropbefore, dropafter, truncate, flip, none
	Arg    string
}

// Parse reads the value of FaultEnv.
func ParseFault(s string) Fault {
	if s == "" {
		return Fault{}
	}
	pt, act, _ := strings.Cut(s, "=")
	name, arg, _ := strings.Cut(act, ":")
	return Fault{Point: pt, Action: name, Arg: arg}
}

// String is the FaultEnv form.
func (f Fault) String() string {
	if f.Point == "" {
		return ""
	}
	s := f.Point + "=" + f.Action
	if f.Arg != "" {
		s += ":" + f.Arg
	}
	return s
}

// Env is the environment entry for a git process, or nothing.
func (f Fault) Env() []string {
	if f.Point == "" {
		return nil
	}
	return []string{FaultEnv + "=" + f.String()}
}

// FromEnv reads the fault of this process.
func FromEnv() Fault { return ParseFault(os.Getenv(FaultEnv)) }

// Hit applies the fault if it is for this point. "kill" ends the whole
// process group with SIGKILL, the client's git included: the machine died
// mid-push, and nothing of it runs on. "error" returns ErrInjected. "sleep"
// delays (the arg is a Go duration).
func (f Fault) Hit(point string) error {
	if f.Point != point {
		return nil
	}
	switch f.Action {
	case "kill":
		_ = syscall.Kill(-syscall.Getpgrp(), syscall.SIGKILL)
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {} // not reached
	case "error":
		return fmt.Errorf("%w at %s", ErrInjected, point)
	case "sleep":
		d, _ := time.ParseDuration(f.Arg)
		time.Sleep(d)
	}
	return nil
}

// Is reports whether the fault is at point with the action.
func (f Fault) Is(point, action string) bool { return f.Point == point && f.Action == action }

// argInt reads the arg as an integer.
func (f Fault) argInt() int { n, _ := strconv.Atoi(f.Arg); return n }

// The named points of a push, in order. Every one is a place where a process
// can die or fail; the spike injects at each.
const (
	PtPreReceive     = "pre-receive:start"        // objects in quarantine, nothing uploaded
	PtAfterUpload    = "pre-receive:after-upload" // pack in the blob store, not registered
	PtAfterRegister  = "pre-receive:after-register"
	PtPrepared       = "prepared:start" // locks held, nothing committed
	PtTxAfterCheck   = "tx:after-check" // inside the transaction, before any write
	PtTxAfterRefs    = "tx:after-refs"
	PtTxAfterPack    = "tx:after-pack-live"
	PtTxBeforeCommit = "tx:before-commit"
	PtTxAfterCommit  = "tx:after-commit" // committed, the answer not yet used
	PtCommitted      = "committed:start" // git moved the local ref
	PtPostReceive    = "post-receive:start"
)

// PushPoints are the kill points of a push, in order.
var PushPoints = []string{PtPreReceive, PtAfterUpload, PtAfterRegister, PtPrepared, PtTxAfterCheck, PtTxAfterRefs,
	PtTxAfterPack, PtTxBeforeCommit, PtTxAfterCommit, PtCommitted, PtPostReceive}
