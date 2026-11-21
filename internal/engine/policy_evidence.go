package engine

import (
	"slices"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// stepEvidence is what the steps before stepID (every step it needs,
// directly or not) reported, and the image references deployDef writes.
func (c *cmd) stepEvidence(stepID string, deployDef *pipeline.Step, floor string) (*policy.Evidence, error) {
	st := c.st
	up := st.Pipeline.Upstream(stepID)
	ev := &policy.Evidence{Steps: []policy.StepFact{}, Deployed: []policy.DeployedRef{}, FindingsListed: true,
		AllowedFailures: []string{}}
	type att struct {
		run, step string
		attempt   int
	}
	var withFindings []att
	for _, id := range st.Order {
		if !up[id] {
			continue
		}
		def := st.Pipeline.Step(id)
		s := st.Steps[id]
		fact := policy.StepFact{Step: id, Kind: def.Kind, Status: string(s.Status), Run: st.ID, RunNumber: st.Number, Attempt: s.Attempt,
			Allowed: s.Status == StepFailed && s.Allowed}
		if fact.Allowed {
			ev.AllowedFailures = append(ev.AllowedFailures, id)
		}
		if r := def.Reports; r != nil {
			if len(r.Tests) > 0 {
				fact.Declared = append(fact.Declared, "tests")
			}
			if len(r.Findings) > 0 {
				fact.Declared = append(fact.Declared, "findings")
			}
			if len(r.SBOM) > 0 {
				fact.Declared = append(fact.Declared, "sbom")
			}
		}
		evid := s.Evidence
		if s.ReusedFrom != "" {
			orig, err := c.reusedState(s.ReusedFrom)
			if err != nil {
				return nil, err
			}
			fact.Run, fact.RunNumber, fact.Attempt, evid = s.ReusedFrom, s.ReusedNumber, s.ReusedAttempt, nil
			if orig != nil && orig.Steps[id] != nil && orig.Steps[id].Attempt == s.ReusedAttempt {
				evid = orig.Steps[id].Evidence
			}
		}
		if def.IsApproval() && s.Status == StepSucceeded {
			fact.Approval = &policy.ApprovalFact{By: s.DecidedBy, At: s.DecidedAt}
		}
		if evid != nil {
			if t := evid.Tests; t != nil {
				fact.Tests = &policy.TestFact{Total: t.Total, Passed: t.Passed, Failed: t.Failed, Errored: t.Errored, Skipped: t.Skipped}
			}
			if f := evid.Findings; f != nil {
				ff := &policy.FindingsFact{Counts: countsOf(f.FindingCounts), Tools: slices.Clone(f.Tools)}
				if f.ByTool != nil {
					ff.ByTool = map[string]policy.Counts{}
					for k, v := range f.ByTool {
						ff.ByTool[k] = countsOf(v)
					}
				}
				if ff.Tools == nil {
					ff.Tools = []string{}
				}
				fact.Findings = ff
				if f.Stored < f.Total || f.Truncated {
					ev.FindingsListed = false
				}
				withFindings = append(withFindings, att{fact.Run, id, fact.Attempt})
			}
			if b := evid.SBOM; b != nil {
				fact.SBOM = &policy.SBOMFact{Components: b.Components}
			}
			if im := evid.Image; im != nil {
				bi := &policy.BuiltImage{Digest: im.Digest, Pushed: im.Pushed, Signed: im.Signed, KeySource: im.KeySource,
					Scanned: im.Scan != nil, ScanFailed: im.Scan != nil && im.Scan.Failed}
				for _, pl := range im.Platforms {
					if pl.Digest != im.Digest {
						bi.Platforms = append(bi.Platforms, pl.Digest)
					}
				}
				fact.Image = bi
			}
		}
		ev.Steps = append(ev.Steps, fact)
	}
	// Findings a waiver might name: those worse than the most lenient
	// threshold, as far as the run kept them.
	if floor != "" {
		for _, a := range withFindings {
			if err := c.addFindingFacts(ev, a.run, a.step, a.attempt, floor); err != nil {
				return nil, err
			}
		}
	}
	if deployDef != nil && deployDef.ArgoCD != nil && deployDef.ArgoCD.WriteBack != nil {
		ev.Changes = len(deployDef.ArgoCD.WriteBack.Changes)
		if changes, err := c.resolveChanges(deployDef); err == nil {
			for _, ch := range changes {
				if ch.Image != "" && (ch.Digest != "" || ch.Tag != "") {
					ev.Deployed = append(ev.Deployed, policy.DeployedRef{File: ch.File, Image: ch.Image, Digest: ch.Digest, Tag: ch.Tag})
				}
			}
		}
	}
	return ev, nil
}

// addFindingFacts adds to ev the findings of one step attempt that are worse
// than floor, as far as the run kept them and up to the most it lists.
func (c *cmd) addFindingFacts(ev *policy.Evidence, run, step string, attempt int, floor string) error {
	rows, err := c.tx.Query(`SELECT fingerprint, tool, rule_id, severity, file, start_line FROM findings
		WHERE run_id = ? AND step_id = ? AND attempt = ? ORDER BY id`, run, step, attempt)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f policy.FindingFact
		if err := rows.Scan(&f.Fingerprint, &f.Tool, &f.Rule, &f.Severity, &f.File, &f.Line); err != nil {
			return err
		}
		if !policy.Worse(f.Severity, floor) {
			continue
		}
		if len(ev.Findings) >= policy.MaxListedFacts {
			ev.FindingsListed = false
			break
		}
		f.Step = step
		ev.Findings = append(ev.Findings, f)
	}
	return rows.Err()
}
