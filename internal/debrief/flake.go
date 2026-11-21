package debrief

import (
	"slices"
	"sort"
)

// Observation is one run's result for one test, oldest first when given to
// Score. Retried says the run passed it only after a retry (a retried step
// or the test tool running it again), so that the same commit saw both.
type Observation struct {
	Run     string
	Number  int
	SHA     string
	Ref     string
	At      int64
	Status  string // passed, failed (errored counts as failed) or skipped
	Retried bool
}

// FlakeVerdict is what the history says about a test.
type FlakeVerdict struct {
	Flaky      bool
	Score      int // 0 to 100, how often the test went against its own run of results
	Reason     string
	Commits    int // distinct commits it ran on
	SameCommit int // commits on which it both passed and failed
	Blips      int // single-commit failures between passes, with no retry
	Branches   []string
	FirstSeen  int64 // the earliest observation that shows it flaky
}

// MinCommitsForFlips is how many commits a test needs before alternating
// results alone (without the same commit passing and failing) make it
// flaky; fewer is too little to tell a flake from a fix.
const MinCommitsForFlips = 4

// Score reads a test's history, oldest first, for flakiness. The rules:
//
//   - the same commit both passed and failed the test (two runs, or a run
//     that passed it on a retry): flaky, whatever else is true, since
//     nothing about the code differed;
//   - otherwise, a test is flaky when its results alternate: it fails on a
//     single commit and passes on both the commit before and the commit
//     after (a blip), at least twice. One failing stretch that ends is a
//     fix, and two or more commits in a row failing is a break, never a
//     flake.
//
// The score is the share of the commits that showed one of these (a mixed
// commit, or a blip), times 100, never over 100 and never less than 1 for
// a flaky test.
func Score(history []Observation) FlakeVerdict {
	var v FlakeVerdict
	type commit struct {
		pass, fail, retried bool
		first               int64
		ref                 string
	}
	order := []string{}
	byCommit := map[string]*commit{}
	branches := map[string]bool{}
	for _, o := range history {
		if o.Status != Passed && o.Status != Failed && o.Status != Errored {
			continue
		}
		c := byCommit[o.SHA]
		if c == nil {
			c = &commit{first: o.At, ref: o.Ref}
			byCommit[o.SHA] = c
			order = append(order, o.SHA)
		}
		if o.Status == Passed {
			c.pass = true
		} else {
			c.fail = true
		}
		if o.Retried {
			c.retried, c.pass, c.fail = true, true, true
		}
	}
	v.Commits = len(order)
	if v.Commits == 0 {
		return v
	}
	mark := func(c *commit) {
		if v.FirstSeen == 0 || c.first < v.FirstSeen {
			v.FirstSeen = c.first
		}
		if c.ref != "" {
			branches[c.ref] = true
		}
	}
	// Per commit result: pass-only, fail-only, or mixed.
	for _, sha := range order {
		if c := byCommit[sha]; c.pass && c.fail {
			v.SameCommit++
			mark(c)
		}
	}
	for i := 1; i+1 < len(order); i++ {
		prev, cur, next := byCommit[order[i-1]], byCommit[order[i]], byCommit[order[i+1]]
		if cur.fail && !cur.pass && prev.pass && !prev.fail && next.pass && !next.fail {
			v.Blips++
			mark(cur)
		}
	}
	switch {
	case v.SameCommit > 0:
		v.Flaky = true
		v.Reason = "passed and failed on the same commit"
	case v.Blips >= 2 && v.Commits >= MinCommitsForFlips:
		v.Flaky = true
		v.Reason = "failed on single commits between passing ones"
	}
	if v.Flaky {
		v.Score = min(100, max(1, (v.SameCommit+v.Blips)*100/v.Commits))
		v.Branches = slices.Sorted(maps(branches))
		sort.Strings(v.Branches)
	}
	return v
}

func maps(m map[string]bool) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
