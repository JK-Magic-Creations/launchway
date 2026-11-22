package engine

// Waiting for room (schema 37). A step that cannot start because there is no
// room for it right now (the step namespace's quota is used up, the cluster
// has no node with space) is not a failure: the runner says it is waiting,
// and the server queues the step again after a pause that grows, until it
// starts or the longest wait the installation allows is over. The wait is
// recorded in the step.queued event (a stable code beside the sentence), so
// the run page, the API, the command line and MCP all say what the step waits
// for, and for how long it has.

import (
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/metrics"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// OutcomeWaiting is the outcome of a step a runner could not start for lack of
// room. It ran nothing; the step is queued again with a pause. Older servers
// read it as failed.
const OutcomeWaiting = "waiting"

// Codes of the reasons a queued step or run waits, as the API gives them
// beside the sentence.
const (
	WaitQuotaFull          = "quota_full"      // the step namespace's quota has no room
	WaitClusterFull        = "cluster_full"    // no node has room
	WaitNoRunner           = "no_runner"       // no runner that may take the step has been seen lately
	WaitRunnersBusy        = "runners_busy"    // every runner that may take the step is busy
	WaitBackoff            = "retry_pause"     // a retry waits out its pause
	WaitRunLimit           = "run_limit"       // the run waits for a limit on concurrent runs
	WaitRunHeld            = "held"            // an operator held the run
	WaitBehindPriority     = "behind_priority" // waits behind runs of higher priority
	DefaultMaxCapacityWait = 60 * time.Minute  // the longest a step waits for room
)

// capacity backoff: the pause after the first try, doubling to the cap.
const (
	capacityPauseFirst = 10 * time.Second
	capacityPauseCap   = time.Minute
)

// capacityPause is the pause after the nth wait (1 is the first).
func capacityPause(n int) time.Duration {
	d := capacityPauseFirst
	for i := 1; i < n && d < capacityPauseCap; i++ {
		d *= 2
	}
	return min(d, capacityPauseCap)
}

// waitForRoom handles a step attempt its runner could not start for lack of
// room: queue it again after a pause, or fail it when it has waited longer
// than the installation allows.
func (c *cmd) waitForRoom(l leaseRef, s *StepState, res Result) {
	stepID := l.stepID
	if !c.e.knows(schemaQueues) {
		c.lose(stepID, nonEmpty(res.Error, "there was no room to start the step"))
		return
	}
	c.dropTests(stepID, l.attempt)
	reason := nonEmpty(res.Error, "there is no room to start the step yet")
	code := nonEmpty(res.WaitCode, WaitQuotaFull)
	if c.st.CancelRequested {
		c.emit(StepCanceled, Payload{Step: stepID, Attempt: s.Attempt, Reason: c.st.stoppedBecause()})
		return
	}
	first := s.WaitSince
	if first == 0 {
		first = c.now
	}
	longest := c.e.maxCapacityWait(c.st.Project)
	if longest > 0 && c.now-first >= longest.Milliseconds() {
		c.emit(StepFinished, Payload{Step: stepID, Attempt: s.Attempt, Outcome: string(StepFailed), Cause: pipeline.CauseInfrastructure,
			Reason: fmt.Sprintf("gave up waiting for room after %s: %s. Free up room (cancel runs that are not needed or add quota to the step namespace), or let steps wait longer with queues.max_wait",
				pipeline.Duration(time.Duration(c.now-first)*time.Millisecond).Short(), strings.TrimPrefix(reason, "waiting for ")),
			WaitCode: code})
		return
	}
	reasonLabel := "other" // a Station chose the code: only the known two become labels
	if code == WaitQuotaFull || code == WaitClusterFull {
		reasonLabel = code
	}
	metrics.StepRoomWaits.Inc(reasonLabel)
	pause := capacityPause(s.WaitTries + 1)
	c.emit(StepQueued, Payload{Step: stepID, Attempt: s.Attempt, Reason: reason, WaitCode: code,
		NotBefore: c.now + pause.Milliseconds(), Requires: schemaQueues})
}

// maxCapacityWait is the longest a step waits for room, from the setting
// queues.max_wait (zero in the setting means the default; a negative value in
// the setting, written "off", means forever).
func (e *Engine) maxCapacityWait(project string) time.Duration {
	if w := e.queueSettings(project).MaxWait; w != 0 {
		return w
	}
	return DefaultMaxCapacityWait
}

// persistWaits keeps step_waits, which orders the steps that wait for room.
func (c *cmd) persistWaits(changed []string) error {
	for _, id := range changed {
		s := c.st.Steps[id]
		if s.Status == StepQueuedS && s.WaitCode != "" {
			if _, err := c.tx.Exec(`INSERT INTO step_waits(run_id, step_id, code, reason, since, first_since, tries) VALUES(?,?,?,?,?,?,?)
				ON CONFLICT(run_id, step_id) DO UPDATE SET code = excluded.code, reason = excluded.reason, since = excluded.since, tries = excluded.tries`,
				c.st.ID, id, s.WaitCode, s.WaitReason, s.QueuedAt, s.WaitSince, s.WaitTries); err != nil {
				return err
			}
			continue
		}
		if _, err := c.tx.Exec(`DELETE FROM step_waits WHERE run_id = ? AND step_id = ?`, c.st.ID, id); err != nil {
			return err
		}
	}
	return nil
}

// explainWait is the sentence a queued step shows: what it waits for, who
// holds the room, and where it is in line.
func (e *Engine) explainWait(st *RunState, s *StepState) string {
	text := s.WaitReason
	if text == "" || !e.knows(schemaQueues) {
		return text
	}
	if holders := e.holdersOfRoom(st); holders != "" {
		text += "; held by " + holders
	}
	rows, err := e.db.Query(`SELECT w.run_id, r.number FROM step_waits w
		JOIN runs r ON r.id = w.run_id LEFT JOIN run_queue q ON q.run_id = w.run_id
		WHERE w.code IN (?, ?) ORDER BY COALESCE(q.priority, 0) DESC, w.first_since, r.number`, WaitQuotaFull, WaitClusterFull)
	if err != nil {
		return text
	}
	defer rows.Close()
	var ahead []int
	for rows.Next() {
		var id string
		var n int
		if rows.Scan(&id, &n) != nil {
			return text
		}
		if id == st.ID {
			break
		}
		ahead = append(ahead, n)
	}
	if rows.Err() != nil {
		return text
	}
	switch len(ahead) {
	case 0:
		text += "; this run is next"
	case 1:
		text += fmt.Sprintf("; this run is next after #%d", ahead[0])
	default:
		text += fmt.Sprintf("; this run is next after #%d and %d more", ahead[0], len(ahead)-1)
	}
	return text
}

// holdersOfRoom names the runs of the project that have a step on a runner
// now: "runs #43–#50".
func (e *Engine) holdersOfRoom(st *RunState) string {
	rows, err := e.db.Query(`SELECT DISTINCT r.number FROM leases l JOIN runs r ON r.id = l.run_id
		WHERE r.project = ? AND l.run_id <> ? ORDER BY r.number`, st.Project, st.ID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var nums []int
	for rows.Next() {
		var n int
		if rows.Scan(&n) != nil {
			return ""
		}
		nums = append(nums, n)
	}
	if rows.Err() != nil || len(nums) == 0 {
		return ""
	}
	slices.Sort(nums)
	return "run" + pluralS(len(nums)) + " " + numberRanges(nums)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// numberRanges writes sorted run numbers as "#43–#50, #52".
func numberRanges(nums []int) string {
	var parts []string
	for i := 0; i < len(nums); {
		j := i
		for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
			j++
		}
		switch j - i {
		case 0:
			parts = append(parts, fmt.Sprintf("#%d", nums[i]))
		case 1:
			parts = append(parts, fmt.Sprintf("#%d, #%d", nums[i], nums[j]))
		default:
			parts = append(parts, fmt.Sprintf("#%d–#%d", nums[i], nums[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}
