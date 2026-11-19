package engine

import (
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// How schema 12 (artifacts, findings and SBOMs) is wired into the engine's
// commands. It is kept here, apart from engine.go, so that the hooks there
// stay one line each.

// FeatureArtifacts is what a runner that keeps and fetches artifacts and
// collects SARIF files says it can do (RunnerInfo.Features). A step that
// needs any of that is only leased to such a runner: an older one would
// run it without the files it fetches.
const FeatureArtifacts = "artifacts"

// schema12Data is what a command keeps to store with its events.
type schema12Data struct {
	findings map[stepAttempt]*keptFindings
	sbom     map[stepAttempt]*keptSBOM
	// dropped are attempts whose runner failed: nothing they kept counts.
	dropped map[stepAttempt]bool
}

func (c *cmd) s12() *schema12Data {
	if c.schema12 == nil {
		c.schema12 = &schema12Data{findings: map[stepAttempt]*keptFindings{}, sbom: map[stepAttempt]*keptSBOM{},
			dropped: map[stepAttempt]bool{}}
	}
	return c.schema12
}

// needsArtifacts reports whether a step needs a runner that knows
// artifacts.
func needsArtifacts(def *pipeline.Step) bool {
	return def != nil && (len(def.Artifacts) > 0 || len(def.Fetch) > 0 ||
		(def.Reports != nil && (len(def.Reports.Findings) > 0 || len(def.Reports.SBOM) > 0)))
}

// runnerCan reports whether a runner can run a step: whether it knows
// artifacts, when the step needs them.
func (c *cmd) runnerCan(r RunnerInfo, stepID string) bool {
	def := c.st.Pipeline.Step(stepID)
	if c.st.Pipeline.HasImageSteps() && !r.Has(FeatureImages) {
		// A run's steps share one runner's workspace, and an image step
		// builds from it: a run with image steps runs, from its first step,
		// on a runner that can build them.
		return false
	}
	if !c.e.knows(schemaArtifacts) || !needsArtifacts(def) {
		return true
	}
	return r.Has(FeatureArtifacts)
}

// Has reports whether a runner says it has a feature.
func (r RunnerInfo) Has(feature string) bool {
	return slices.Contains(strings.Fields(r.Features), feature)
}

// prepared12 is what Finish works out before its transaction: the findings
// and SBOM of the attempt, parsed.
type prepared12 struct {
	findings *parsedFindings
	sbom     *parsedSBOM
}

func (e *Engine) prepare12(l leaseRef, res Result) *prepared12 {
	p := &prepared12{}
	if !e.knows(schemaArtifacts) || (res.Outcome != "succeeded" && res.Outcome != "failed" && res.Outcome != "canceled") {
		return p
	}
	if res.Findings != nil {
		p.findings = e.parseFindings(res.Findings)
	}
	p.sbom = e.readSBOM(l)
	return p
}

// finish12 adds to the event that ends an attempt what schema 12 records:
// its findings, SBOM and artifacts.
func (c *cmd) finish12(l leaseRef, res Result, p *Payload, prep *prepared12) error {
	def := c.st.Pipeline.Step(l.stepID)
	if !c.e.knows(schemaArtifacts) || def == nil || prep == nil {
		return nil
	}
	ended := res.Outcome == "succeeded" || res.Outcome == "failed" || res.Outcome == "canceled"
	if !ended {
		return nil
	}
	if def.Reports != nil && len(def.Reports.Findings) > 0 {
		pf := prep.findings
		if pf == nil {
			pf = &parsedFindings{summary: FindingsSummary{Tools: []string{}, Files: []FindingFile{}}}
			pf.summary.note("the runner sent no SARIF files")
		}
		var err error
		if p.Findings, err = c.keepFindings(l.stepID, l.attempt, pf); err != nil {
			return err
		}
	}
	if def.Reports != nil && len(def.Reports.SBOM) > 0 {
		ps := prep.sbom
		if ps == nil {
			ps = &parsedSBOM{summary: SBOMSummary{Files: []SBOMFile{}, Licences: map[string]int{}}}
			ps.summary.note("no SBOM files were kept; the step's log says why")
		}
		var err error
		if p.SBOM, err = c.keepSBOM(l.stepID, l.attempt, ps); err != nil {
			return err
		}
	}
	if len(def.Artifacts) > 0 || (def.Reports != nil && len(def.Reports.SBOM) > 0) {
		list, err := attemptArtifacts(c.tx, l.runID, l.stepID, l.attempt)
		if err != nil {
			return err
		}
		p.Artifacts = list
	}
	return nil
}

// drop12 forgets what an attempt kept, because it is not recorded as having
// ended after all (its runner failed): its artifacts end, and its findings
// and SBOM are not stored.
func (c *cmd) drop12(step string, attempt int) {
	d := c.s12()
	k := stepAttempt{step, attempt}
	delete(d.findings, k)
	delete(d.sbom, k)
	d.dropped[k] = true
}

// persistSchema12 writes what schema 12 added to the derived tables.
func (c *cmd) persistSchema12() error {
	st := c.st
	for _, ev := range c.events {
		p := ev.Data
		switch ev.Type {
		case StepFinished, StepCanceled:
			if p.Findings != nil {
				if err := c.persistFindings(ev.At, p.Step, p.Attempt, p.Findings); err != nil {
					return err
				}
			}
			if p.SBOM != nil {
				if err := c.persistSBOM(ev.At, p.Step, p.Attempt, p.SBOM); err != nil {
					return err
				}
			}
			def := st.Pipeline.Step(p.Step)
			if !needsArtifacts(def) || p.Attempt == 0 {
				continue
			}
			k := stepAttempt{p.Step, p.Attempt}
			statuses := []string{artUploading}
			note := "the step ended before the upload completed"
			if c.schema12 != nil && c.schema12.dropped[k] {
				statuses = []string{artUploading, artKept}
				note = "the runner failed during the step, so nothing it kept counts"
			}
			if err := endAttemptArtifacts(c.tx, st.ID, p.Step, p.Attempt, statuses, artFailed, note, ev.At); err != nil {
				return err
			}
		case StepLost:
			if !needsArtifacts(st.Pipeline.Step(p.Step)) {
				continue
			}
			if err := endAttemptArtifacts(c.tx, st.ID, p.Step, p.Attempt, []string{artUploading, artKept}, artFailed,
				"the runner stopped responding or failed, so nothing it kept counts", ev.At); err != nil {
				return err
			}
		}
	}
	return nil
}

// attachSchema12 adds finding counts and artifact counts to runs as
// listed.
func (e *Engine) attachSchema12(runs []*RunSummary) error {
	if err := e.attachFindingCounts(runs); err != nil {
		return err
	}
	return e.attachArtifactCounts(runs)
}

// CheckArtifactOptions fills in and checks artifact options, as Open does.
func CheckArtifactOptions(o ArtifactOptions) (ArtifactOptions, error) { return o.withDefaults() }

// CheckFindingLimits fills in and checks finding limits, as Open does.
func CheckFindingLimits(l FindingLimits) (FindingLimits, error) { return l.withDefaults() }
