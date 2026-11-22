package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// asker decides approval steps in a local run: it asks on the terminal, one
// at a time, or rejects them if there is no terminal to ask on.
type asker struct {
	eng      *engine.Engine
	runID    string
	actor    string
	pr       *printer
	lines    chan string // what is typed on stdin; nil if stdin is not a terminal
	asked    map[string]bool
	question string // the step whose answer is awaited, or ""
}

func newAsker(eng *engine.Engine, runID, actor string, pr *printer) *asker {
	a := &asker{eng: eng, runID: runID, actor: actor, pr: pr, asked: map[string]bool{}}
	if isTerminal(os.Stdin) {
		a.lines = make(chan string)
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				a.lines <- sc.Text()
			}
			close(a.lines)
		}()
	}
	return a
}

// update asks about the next approval that is waiting, if nothing is being
// asked already, and notices when the one being asked about stops waiting.
func (a *asker) update(d *engine.RunDetail) {
	for i := range d.StepList {
		s := &d.StepList[i]
		if s.ID == a.question && s.Status != engine.StepWaitingS {
			fmt.Println() // it was decided another way (canceled, or it expired)
			a.question = ""
		}
	}
	for i := range d.StepList {
		s := &d.StepList[i]
		key := fmt.Sprintf("%s#%d", s.ID, s.Attempt)
		if a.question != "" || s.Status != engine.StepWaitingS || a.asked[key] {
			continue
		}
		a.asked[key] = true
		if a.lines == nil {
			a.decide(s.ID, false, byCommand, "approvals need a terminal to answer in, or a Launchway server")
			continue
		}
		a.question = s.ID
		fmt.Print(a.pr.prefix(s.ID) + "Approve? [y/N] ")
	}
}

// answers is where typed answers arrive while a question is open.
func (a *asker) answers() <-chan string {
	if a.question == "" {
		return nil
	}
	return a.lines
}

func (a *asker) answer(line string, ok bool) {
	id := a.question
	a.question = ""
	if !ok {
		fmt.Println()
		a.lines = nil // stdin is closed: nobody can answer any more
		a.decide(id, false, byCommand, "stdin was closed before anyone answered")
		return
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		a.decide(id, true, a.actor, "")
	default:
		a.decide(id, false, a.actor, "")
	}
}

// byCommand is the actor recorded when nobody could be asked.
const byCommand = "launchway run"

func (a *asker) decide(stepID string, approve bool, actor, comment string) {
	if err := a.eng.Decide(a.runID, stepID, approve, actor, comment); err != nil {
		fmt.Fprintln(os.Stderr, a.pr.prefix(stepID)+err.Error())
	}
}
