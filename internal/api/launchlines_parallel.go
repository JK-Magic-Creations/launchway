package api

import "github.com/jk-magic-creations/launchway/internal/engine"

// Parallel says what a station that stands for several steps is made of, so a compact line shows
// parallelism honestly: "3 at once: 2 passed, 1 running". It is set only for a station with more than one step.
type Parallel struct {
	// Total is how many steps the station stands for. Together is the most of them that sit in the same
	// stage of the pipeline (they do not wait for each other, so they can run at the same time); 1 means a chain.
	Total    int `json:"total"`
	Together int `json:"together"`
	Done     int `json:"done"`
	Running  int `json:"running"`
	Waiting  int `json:"waiting"`
	Failed   int `json:"failed"`
	Skipped  int `json:"skipped"`
	Idle     int `json:"idle"`
	// Steps lists them, in pipeline order, each with its state as a station state.
	Steps []ParallelStep `json:"steps"`
}

// ParallelStep is one step of a station that stands for several.
type ParallelStep struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// stationStepState maps a step's status to a station state.
func stationStepState(st engine.StepView) string {
	switch st.Status {
	case engine.StepFailed:
		if st.AllowedFailure {
			return stationDone
		}
		return stationFailed
	case engine.StepWaitingS:
		return stationWaiting
	case engine.StepRunning, engine.StepQueuedS, engine.StepLeasedS:
		return stationRunning
	case engine.StepSucceeded:
		return stationDone
	case engine.StepSkippedS, engine.StepCanceledS:
		return stationSkipped
	}
	return stationIdle
}

// parallelOf describes the steps behind a station, or nil when it stands for one. levels is the run's stages.
func parallelOf(steps []engine.StepView, levels [][]string) *Parallel {
	if len(steps) < 2 {
		return nil
	}
	levelOf := map[string]int{}
	for i, l := range levels {
		for _, id := range l {
			levelOf[id] = i
		}
	}
	p := &Parallel{Total: len(steps), Steps: make([]ParallelStep, 0, len(steps))}
	perLevel := map[int]int{}
	for _, st := range steps {
		state := stationStepState(st)
		p.Steps = append(p.Steps, ParallelStep{ID: st.ID, State: state})
		switch state {
		case stationDone:
			p.Done++
		case stationRunning:
			p.Running++
		case stationWaiting:
			p.Waiting++
		case stationFailed:
			p.Failed++
		case stationSkipped:
			p.Skipped++
		default:
			p.Idle++
		}
		perLevel[levelOf[st.ID]]++
	}
	p.Together = 1
	for _, n := range perLevel {
		p.Together = max(p.Together, n)
	}
	return p
}
