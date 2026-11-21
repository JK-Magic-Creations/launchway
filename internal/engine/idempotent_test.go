package engine

import (
	"errors"
	"testing"
	"time"
)

// A runner's report that it did a command, sent again, is not a lost lease.
func TestCommandReportRepeat(t *testing.T) {
	taken := &CommandState{State: CommandTaken, Take: "t1"}
	if ok, err := commandReport(taken, CommandResult{Take: "t1", Result: "applied"}); !ok || err != nil {
		t.Fatalf("the first report acts: %v %v", ok, err)
	}
	done := &CommandState{State: CommandDone, Take: "t1", Result: "applied"}
	if ok, err := commandReport(done, CommandResult{Take: "t1", Result: "applied"}); ok || err != nil {
		t.Fatalf("a repeat is a success that does nothing: %v %v", ok, err)
	}
	if _, err := commandReport(done, CommandResult{Take: "t1", Result: "failed"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a different result is refused: %v", err)
	}
	if _, err := commandReport(done, CommandResult{Take: "other", Result: "applied"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("another take is refused: %v", err)
	}
	if _, err := commandReport(nil, CommandResult{Take: "t1"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("no command: %v", err)
	}
}

// The first answer to a keyed call is kept and old ones are pruned.
func TestRememberWriteKeepsTheFirstAnswer(t *testing.T) {
	e, clk := newEngine(t)
	if err := e.RememberWrite("k", 200, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.RememberWrite("k", 200, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if st, body, ok := e.RecallWrite("k"); !ok || st != 200 || string(body) != `{"a":1}` {
		t.Fatalf("recalled %d %s %v", st, body, ok)
	}
	clk.Add(writeKeep + time.Minute)
	e.pruneWrites()
	if _, _, ok := e.RecallWrite("k"); ok {
		t.Fatal("an answer older than the retention should be pruned")
	}
}
