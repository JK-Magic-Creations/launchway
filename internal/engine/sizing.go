package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/resources"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// Automatic sizing, the first cut of docs/development/automatic-resources.md:
// a step that declares no memory of its own is given what the same step
// used in earlier runs of the Flight, with headroom, and a step killed for
// memory is run once more with twice the memory.
//
// It is deliberately conservative:
//
//   - Numbers written in a file (the step's resources, a stage's, a plan's, or
//     the memory of its container under containers) are never changed.
//   - A learned figure is never below the installation's default for the
//     kind, so learning only ever raises; and it needs the installation to
//     size steps at all (a default with memory), so an installation that has
//     not opted in to sizing is unchanged.
//   - It is never above the installation's step maximum (max_memory), or,
//     where none is set, twice the default's limit.
//   - It reads at most the last learnWindow measured attempts of the step on
//     the same pipeline of the same Flight and branch family, and trusts them
//     from learnMinRuns on.

// Where a step's numbers came from.
const (
	SizingDeclared = "declared" // written in the pipeline file
	SizingLearned  = "learned"  // from the step's recorded peaks
	SizingDefault  = "default"  // the installation's default
	SizingRaised   = "raised"   // doubled after the step was killed for memory
)

const (
	learnWindow   = 20
	learnMinRuns  = 3
	learnHeadroom = 25 // percent over the 95th percentile of the peaks
	learnRound    = 64 << 20
)

// Sizing says where a step's memory and CPU came from.
type Sizing struct {
	Source string `json:"source"`
	// Runs is how many measured runs a learned figure rests on (for a default,
	// how many were measured and stayed within it).
	Runs int `json:"runs,omitempty"`
	// Text is the sentence the log and the run page show.
	Text string `json:"text,omitempty"`
}

// Words writes where the numbers came from as the run page and runs show say it.
func (s Sizing) Words() string {
	switch s.Source {
	case SizingLearned:
		return fmt.Sprintf("learned from %d runs", s.Runs)
	case SizingRaised:
		return "raised after a memory kill"
	case SizingDeclared:
		return "declared in the pipeline file"
	case SizingDefault:
		if s.Runs > 0 {
			return fmt.Sprintf("default (%d runs measured, all within it)", s.Runs)
		}
		return "default"
	}
	return ""
}

// branchFamily groups branches whose runs are alike: feat/a and feat/b are
// the family feat, main is main. A pull request run has no branch of its own
// here and is the family of the branch it comes from (or "").
func branchFamily(branch string) string {
	if i := strings.IndexByte(branch, '/'); i > 0 {
		return branch[:i]
	}
	return branch
}

// learnMemory turns recorded peaks (any order) into a request and a limit.
// ok is false when there are too few peaks or what they ask for is within
// what the default already gives. Nothing is ever below floorReq; the result
// is never above capBytes (0: no cap).
func learnMemory(peaks []int64, floorReq, floorLimit, capBytes int64) (req, limit int64, ok bool) {
	if len(peaks) < learnMinRuns {
		return 0, 0, false
	}
	s := slices.Clone(peaks)
	slices.Sort(s)
	p95 := s[(len(s)*95+99)/100-1]
	need := p95 + p95*learnHeadroom/100
	need = (need + learnRound - 1) / learnRound * learnRound
	if need <= floorReq {
		return 0, 0, false
	}
	req, limit = need, max(need, floorLimit)
	if capBytes > 0 && limit > capBytes {
		limit = capBytes
		req = min(req, capBytes)
	}
	return req, limit, true
}

// memoryCap is the most automatic sizing may give a step.
func memoryCap(pol resourcePolicy, def resources.Amount) int64 {
	if pol.limits.MaxMemory > 0 {
		return pol.limits.MaxMemory
	}
	return 2 * max(def.Memory, def.MemoryLimit)
}

// memoryDeclared says whether the file gives the step a memory of its own.
func memoryDeclared(def *pipeline.Step, declared resources.Request, container *pipeline.Container) bool {
	if declared.Memory != "" || declared.MemoryLimit != "" {
		return true
	}
	return container != nil && container.Memory != ""
}

// MemoryKilled says whether an attempt's result is a kill for memory: the
// runner's own words for one (an exit code of 137 with the explanation of
// the executor).
func memoryKilled(res Result) bool {
	if res.Outcome != "failed" {
		return false
	}
	text := res.Error
	return strings.Contains(text, "used more memory than") || (res.ExitCode != nil && *res.ExitCode == 137 && strings.Contains(text, "memory"))
}

// autoSize gives the step's container learned or raised memory where the file
// declares none, and says where its numbers came from. ct is the step's own
// container as stepContainer sized it from the defaults.
func (c *cmd) autoSize(def *pipeline.Step, ct *pipeline.Container, orig *pipeline.Container, pol resourcePolicy) Sizing {
	var declared resources.Request
	if def.Resources != nil {
		declared = *def.Resources
	}
	if !c.e.knows(schemaSizing) {
		return Sizing{Source: SizingDefault} // a database from before the sizing columns: nothing is learned
	}
	if memoryDeclared(def, declared, orig) {
		return Sizing{Source: SizingDeclared}
	}
	base := pol.defaults[def.ResourceKind()]
	if base.Memory <= 0 && base.MemoryLimit <= 0 {
		return Sizing{Source: SizingDefault} // the installation does not size steps
	}
	floorReq, floorLimit := max(base.Memory, 0), max(base.MemoryLimit, base.Memory)
	capBytes := memoryCap(pol, base)
	set := func(req, limit int64) {
		ct.Memory, ct.MemoryLimit = resources.FormatMemory(req), resources.FormatMemory(limit)
		cpuLimit, _ := resources.ParseCPU(ct.CPULimit)
		ct.Threads = resources.Threads(cpuLimit, limit, pol.limits.MemoryPerThread)
	}
	// An earlier attempt of this step in this run was killed for memory: it
	// runs with twice what that one had, once.
	if rows, err := store.StepUsages(c.tx, c.st.ID); err == nil {
		var killed *store.UsageRow
		for i := range rows {
			if rows[i].Step == def.ID && rows[i].Killed {
				killed = &rows[i]
			}
		}
		if killed != nil {
			had := max(killed.ReservedMemory, floorLimit)
			next := min(2*had, max(capBytes, had))
			set(max(next, floorReq), next)
			return Sizing{Source: SizingRaised, Text: fmt.Sprintf("memory raised from %s to %s because the earlier attempt was killed for memory", SizeText(had), SizeText(next))}
		}
	}
	peaks, err := store.StepPeaks(c.tx, c.st.Project, c.st.Pipeline.Name, def.ID, 200)
	if err != nil {
		return Sizing{Source: SizingDefault}
	}
	fam := branchFamily(c.st.Trigger.Branch)
	var seen []int64
	for _, p := range peaks {
		if branchFamily(p.Branch) == fam && len(seen) < learnWindow {
			seen = append(seen, p.Bytes)
		}
	}
	if req, limit, ok := learnMemory(seen, floorReq, floorLimit, capBytes); ok {
		set(req, limit)
		return Sizing{Source: SizingLearned, Runs: len(seen), Text: fmt.Sprintf("memory %s (limit %s) learned from the highest use of the last %d runs of this step", SizeText(req), SizeText(limit), len(seen))}
	}
	if len(seen) >= learnMinRuns {
		return Sizing{Source: SizingDefault, Runs: len(seen)}
	}
	return Sizing{Source: SizingDefault}
}

// memoryRetry handles an attempt killed for memory whose numbers were
// automatic: the step runs once more with twice the memory (up to the cap),
// and the log says so. A second kill, a step that declares its own memory and
// a step already at the cap fail as before. It reports whether it queued the
// retry, and has then recorded the attempt as failed.
func (c *cmd) memoryRetry(stepID string, s *StepState, p *Payload, res Result) bool {
	def := c.st.Pipeline.Step(stepID)
	if def == nil || c.st.CancelRequested || !memoryKilled(res) || def.ResourceKind() == "" || !c.e.knows(schemaResources) || !c.e.knows(schemaSizing) {
		return false
	}
	pol, err := c.e.loadPolicy(c.tx)
	if err != nil {
		return false
	}
	base := pol.defaults[def.ResourceKind()]
	if base.Memory <= 0 && base.MemoryLimit <= 0 {
		return false
	}
	var declared resources.Request
	if def.Resources != nil {
		declared = *def.Resources
	}
	if memoryDeclared(def, declared, c.st.Pipeline.Container(c.st.Pipeline.ContainerFor(def))) {
		return false
	}
	rows, err := store.StepUsages(c.tx, c.st.ID)
	if err != nil {
		return false
	}
	kills, had := 0, max(base.MemoryLimit, base.Memory)
	for _, r := range rows {
		if r.Step == stepID && r.Killed {
			kills++
		}
		if r.Step == stepID && r.Attempt == s.Attempt && r.ReservedMemory > 0 {
			had = r.ReservedMemory
		}
	}
	if kills > 1 {
		return false // the one automatic retry was this step's earlier kill
	}
	capBytes := max(memoryCap(pol, base), had)
	if had >= capBytes {
		p.Reason += fmt.Sprintf(" (it already had %s, the most a step may be given, so it is not run again; raise max_memory or ask for less)", SizeText(had))
		return false
	}
	next := min(2*had, capBytes)
	c.emitFailure(p, res, true)
	c.emit(StepQueued, Payload{Step: stepID, Attempt: s.Attempt + 1,
		Reason: fmt.Sprintf("retry: killed for memory at %s; runs again with %s (what it used is kept for the next run)", SizeText(had), SizeText(next))})
	return true
}
