package deploy

import (
	"fmt"
	"strings"
)

// Controlling a rollout: what a command expects of it, whether one can be
// given now, and, on the deploy runner, whether to apply one.
//
// A command (promote, promote-full, abort, retry) is recorded with what it
// expects: the deployment's pod template hash and the canary step the
// rollout was at. The deploy runner patches the rollout only if it is still
// there; one that has moved past that point means the command was already
// applied, and one on another pod template means the deployment's version
// is no longer what the rollout runs, so the command is not applied.

// Actions on a rollout (the same words as pipeline.Action*).
const (
	Promote     = "promote"
	PromoteFull = "promote-full"
	Abort       = "abort"
	Retry       = "retry"
)

// Phases a rollout step adds.
const (
	PhaseCommand = "command_pending" // waiting for a deploy runner to apply a command
)

// Expect is what a command expects of the rollout.
type Expect struct {
	PodHash string `json:"pod_hash"`
	Step    *int   `json:"step,omitempty"`
	// Paused says the rollout was at a pause when the command was given.
	Paused bool `json:"paused,omitempty"`
}

func (x Expect) String() string {
	if x.Step != nil {
		return fmt.Sprintf("%s at step %d", x.PodHash, *x.Step+1)
	}
	return x.PodHash
}

// Command outcomes.
const (
	Applied        = "applied"
	AlreadyApplied = "already_applied"
	NotApplied     = "not_applied"
	CommandFailed  = "failed"
)

// Command is the outcome a Verdict asks for: give the rollout a command.
const Command Outcome = "command"

// OurPodHash is the deployment's pod template hash, as a snapshot shows
// it: only once Argo CD has synced the deployment's commit (the revision
// includes it, and the application, or the Rollout among the resources
// judged, is Synced: Argo CD reports the new revision while still
// applying it) and the rollout controller has seen the new spec is the
// rollout's current pod template the deployment's. "" when that is not
// shown.
func OurPodHash(s Snapshot) string {
	if s.Error != "" || s.App == nil || !isTrue(s.App.Contains) || s.Rollout == nil || !s.Rollout.Found || !s.Rollout.ObservedKnown {
		return ""
	}
	synced := s.App.Sync == "Synced"
	for _, r := range s.App.Resources {
		if r.Kind == "Rollout" && r.Found && r.Sync == "Synced" {
			synced = true
		}
	}
	if !synced {
		return ""
	}
	return s.Rollout.CurrentPodHash
}

// IndefinitePause reports whether the rollout waits at a pause no timer
// ends, and why.
func IndefinitePause(ro *Rollout) (string, bool) {
	if ro == nil || !ro.Found || ro.Aborted || len(ro.PauseReasons) == 0 {
		return "", false
	}
	reason := strings.Join(ro.PauseReasons, ", ")
	if ro.PauseIndefinite {
		return reason, true
	}
	// A deploy runner too old to say: a pause step written without a
	// duration is named "pause".
	if ro.Step != nil && *ro.Step < len(ro.StepNames) && ro.StepNames[*ro.Step] == "pause" && reason == "CanaryPauseStep" {
		return reason, true
	}
	return "", false
}

// completed reports whether the rollout is fully on hash.
func completed(ro *Rollout, hash string) bool {
	return ro.Phase == "Healthy" && ro.StableRS == hash && ro.CurrentPodHash == hash && !ro.Aborted
}

// AbortedOn reports whether a snapshot shows the deployment's version
// aborted. hash is the deployment's pod template hash, if known.
func AbortedOn(s Snapshot, hash string) bool {
	ours := OurPodHash(s)
	if ours == "" || !s.Rollout.Aborted {
		return false
	}
	return hash == "" || ours == hash
}

// Available says whether a command can be given now to a deployment whose
// pod template hash is hash, from the latest snapshot: "" if it can, else
// why not, in plain words. It also returns what the command would expect.
func Available(action string, s Snapshot, hash string) (Expect, string) {
	ro := s.Rollout
	switch {
	case ro == nil || !ro.Found:
		return Expect{}, "the rollout has not been observed"
	case hash == "":
		return Expect{}, "the rollout has not shown this deployment's version yet"
	case ro.CurrentPodHash != hash:
		return Expect{}, fmt.Sprintf("the rollout is on another version (pod template %s), not this deployment's (%s)", ro.CurrentPodHash, hash)
	}
	x := Expect{PodHash: hash, Step: ro.Step, Paused: ro.Paused || len(ro.PauseReasons) > 0}
	done := completed(ro, hash)
	switch action {
	case Promote:
		switch {
		case ro.Aborted:
			return x, "the rollout was aborted; retry it, or roll back"
		case done:
			return x, "the rollout is complete"
		case len(ro.PauseReasons) == 0:
			return x, "the rollout is not paused; promote applies at a pause"
		}
	case PromoteFull:
		switch {
		case ro.Aborted:
			return x, "the rollout was aborted; retry it, or roll back"
		case done:
			return x, "the rollout is complete"
		case ro.PromoteFull:
			return x, "a full promotion is already under way"
		}
	case Abort:
		switch {
		case ro.Aborted:
			return x, "the rollout is already aborted"
		case done:
			return x, "the rollout is complete; aborting does not undo it (a rollback reverts it)"
		}
	case Retry:
		if !ro.Aborted {
			return x, "the rollout is not aborted"
		}
	default:
		return x, fmt.Sprintf("%q is not an action", action)
	}
	return x, ""
}

// Decision is what a deploy runner does with a command, given the rollout
// as it reads it just before.
type Decision struct {
	Apply  bool
	Result string // when not Apply: AlreadyApplied or NotApplied
	Detail string
}

// Decide is the deploy runner's check before it patches: the rollout must
// still be at what the command expects.
func Decide(action string, x Expect, ro *Rollout) Decision {
	switch {
	case ro == nil || !ro.Found:
		return Decision{Result: NotApplied, Detail: "the rollout does not exist"}
	case x.PodHash == "":
		return Decision{Result: NotApplied, Detail: "the command does not say which version of the rollout it is for"}
	case ro.CurrentPodHash != x.PodHash:
		return Decision{Result: NotApplied, Detail: fmt.Sprintf("the rollout moved on to another version (pod template %s); the command was for %s", ro.CurrentPodHash, x.PodHash)}
	}
	done := completed(ro, x.PodHash)
	step := func() int {
		if ro.Step == nil {
			return -1
		}
		return *ro.Step
	}
	want := -1
	if x.Step != nil {
		want = *x.Step
	}
	switch action {
	case Promote:
		switch {
		case ro.Aborted:
			return Decision{Result: NotApplied, Detail: "the rollout was aborted before it could be promoted"}
		case done:
			return Decision{Result: AlreadyApplied, Detail: "the rollout is already complete"}
		case step() > want:
			return Decision{Result: AlreadyApplied, Detail: fmt.Sprintf("the rollout is already past step %d", want+1)}
		case len(ro.PauseReasons) == 0:
			return Decision{Result: AlreadyApplied, Detail: fmt.Sprintf("the rollout is no longer paused at step %d", want+1)}
		}
	case PromoteFull:
		switch {
		case ro.Aborted:
			return Decision{Result: NotApplied, Detail: "the rollout was aborted before it could be promoted"}
		case done, ro.PromoteFull:
			return Decision{Result: AlreadyApplied, Detail: "the rollout is already fully promoted, or being"}
		}
	case Abort:
		switch {
		case ro.Aborted:
			return Decision{Result: AlreadyApplied, Detail: "the rollout is already aborted"}
		case done:
			return Decision{Result: NotApplied, Detail: "the rollout had already completed; aborting does not undo it"}
		}
	case Retry:
		if !ro.Aborted {
			return Decision{Result: AlreadyApplied, Detail: "the rollout is not aborted (it was retried already)"}
		}
	default:
		return Decision{Result: NotApplied, Detail: fmt.Sprintf("this deploy runner does not know the action %q", action)}
	}
	return Decision{Apply: true}
}

// StatusPatch is the status a command sets, as Argo Rollouts' own tools set
// it (verified against Argo Rollouts v1.8.3). A rollout paused through its
// spec (spec.paused) needs a spec change to promote, which Launchway does
// not make.
func StatusPatch(action string) map[string]any {
	switch action {
	case Promote:
		return map[string]any{"pauseConditions": nil}
	case PromoteFull:
		return map[string]any{"promoteFull": true}
	case Abort:
		return map[string]any{"abort": true}
	case Retry:
		return map[string]any{"abort": false}
	}
	return nil
}

// StartAction decides what a rollout step does with a snapshot before it
// has given its command: give it (Outcome Command), wait, or end at once.
// hash is the deployment's pod template hash, if known.
func StartAction(spec Spec, action string, s Snapshot, hash string) Verdict {
	ours := OurPodHash(s)
	if ours == "" {
		v := Evaluate(Spec{Kind: spec.Kind, Commit: spec.Commit, Health: spec.Health, Resources: spec.Resources, Windows: spec.Windows}, s)
		if v.Outcome == Failed {
			return v
		}
		return cont(PhaseRollout, "waiting for the rollout to show this deployment's version before it can %s it", verb(action))
	}
	if hash != "" && ours != hash {
		return fail(PhaseRollout, "the rollout is on another version (pod template %s), not this deployment's (%s)", ours, hash)
	}
	ro := s.Rollout
	x := Expect{PodHash: ours, Step: ro.Step, Paused: len(ro.PauseReasons) > 0}
	give := Verdict{Outcome: Command, Phase: PhaseCommand, Detail: fmt.Sprintf("asking a deploy runner to %s the rollout", verb(action)), Action: action, Expect: &x}
	done := completed(ro, ours)
	switch action {
	case Promote:
		switch {
		case ro.Aborted:
			return fail(PhaseAborted, "the rollout was aborted%s; there is nothing to promote", colon(ro.Message))
		case done:
			return Verdict{Outcome: Succeeded, Phase: PhaseHealthy, Detail: "the rollout is already complete; nothing to promote"}
		case len(ro.PauseReasons) == 0:
			return rolloutProgressNote(s, "waiting for the rollout to pause before promoting it")
		}
	case PromoteFull:
		switch {
		case ro.Aborted:
			return fail(PhaseAborted, "the rollout was aborted%s; there is nothing to promote", colon(ro.Message))
		case done:
			return Verdict{Outcome: Succeeded, Phase: PhaseHealthy, Detail: "the rollout is already complete; nothing to promote"}
		}
	case Abort:
		switch {
		case ro.Aborted:
			return Verdict{Outcome: Succeeded, Phase: PhaseAborted, Detail: "the rollout is already aborted"}
		case done:
			return fail(PhaseHealthy, "the rollout is already complete; aborting does not undo it (a rollback step reverts it)")
		}
	case Retry:
		if !ro.Aborted {
			return AfterAction(spec, action, nil, s)
		}
	}
	return give
}

// AfterAction decides what a rollout step's snapshot means once its
// command was applied (or found applied): an abort is done once the
// rollout is aborted; the others watch until healthy or the next
// indefinite pause.
func AfterAction(spec Spec, action string, x *Expect, s Snapshot) Verdict {
	if action == Abort {
		if ours := OurPodHash(s); ours != "" && s.Rollout.Aborted && (x == nil || ours == x.PodHash) {
			return Verdict{Outcome: Succeeded, Phase: PhaseAborted, Detail: "the rollout is aborted" + colon(s.Rollout.Message)}
		}
		return cont(PhaseRollout, "waiting for the rollout to abort")
	}
	if ours := OurPodHash(s); x != nil && ours != "" && ours != x.PodHash {
		return fail(PhaseRollout, "the rollout moved on to another version (pod template %s) after the %s", ours, verb(action))
	}
	if x != nil && x.Step != nil && action == Promote {
		step := *x.Step
		spec.PastStep = &step
	}
	return Evaluate(spec, s)
}

func verb(action string) string {
	switch action {
	case PromoteFull:
		return "fully promote"
	case "":
		return "act on"
	}
	return action
}

// rolloutProgressNote is rolloutProgress with a note first.
func rolloutProgressNote(s Snapshot, note string) Verdict {
	v := rolloutProgress(s)
	v.Detail = note + "; " + v.Detail
	return v
}

// pauseVerdict is what an indefinite pause means for a step watching
// until healthy or paused. ok is false if it is not such a pause.
func pauseVerdict(spec Spec, s Snapshot) (Verdict, bool) {
	ro := s.Rollout
	reason, ok := IndefinitePause(ro)
	if !ok {
		return Verdict{}, false
	}
	where := "paused"
	if ro.Step != nil && ro.Steps > 0 {
		where = fmt.Sprintf("paused at step %d of %d", min(*ro.Step+1, ro.Steps), ro.Steps)
	}
	until, pauses := spec.Until, spec.Pauses
	if spec.Rollout != nil {
		if until == "" {
			until = spec.Rollout.Until
		}
		if pauses == "" && spec.Kind != "rollout" {
			pauses = spec.Rollout.Pauses
		}
	}
	past := spec.PastStep == nil || ro.Step == nil || *ro.Step > *spec.PastStep
	switch {
	case until == "paused" && past:
		return Verdict{Outcome: Succeeded, Phase: PhasePaused, Detail: fmt.Sprintf("the rollout is %s (%s) on %s", where, reason, ro.CurrentPodHash)}, true
	case until == "paused":
		return cont(PhasePaused, "the rollout is still %s (%s); waiting for it to move on", where, reason), true
	case pauses == "fail":
		return fail(PhasePaused, "the rollout is %s (%s), and the step says pauses: fail", where, reason), true
	case pauses == "promote" && strings.Contains(reason, "Inconclusive"):
		return cont(PhasePaused, "the rollout is %s because an analysis was inconclusive; pauses: promote never promotes past that, so someone must promote or abort it", where), true
	case pauses == "promote" && past:
		x := Expect{PodHash: ro.CurrentPodHash, Step: ro.Step, Paused: true}
		return Verdict{Outcome: Command, Phase: PhasePaused, Detail: fmt.Sprintf("the rollout is %s (%s); promoting it (pauses: promote)", where, reason), Action: Promote, Expect: &x}, true
	}
	return cont(PhasePaused, "the rollout is %s (%s); waiting for someone to promote it", where, reason), true
}
