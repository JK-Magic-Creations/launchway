package runner

import (
	"context"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/verify"
	hs "github.com/jk-magic-creations/launchway/internal/verify/source"
)

// checkReading is what one health check saw in one interval.
type checkReading struct {
	res hs.CheckResult
	err error
}

// startChecks begins the health checks of every interval not yet recorded.
// A check calls the service while its interval runs (not afterwards, as a
// metric is read from a source), so each interval's goroutine waits for its
// start, calls until its end and hands the reading over on its channel.
func (d *DeployRunner) startChecks(ctx context.Context, job *engine.Job) map[int]chan map[string]checkReading {
	vj := job.Verify
	var checks []engine.VerifyQuery
	for _, m := range vj.Metrics {
		if m.Check != nil {
			checks = append(checks, m)
		}
	}
	out := map[int]chan map[string]checkReading{}
	if len(checks) == 0 || d.Sources == nil {
		return out
	}
	for i := 0; i < vj.Intervals; i++ {
		if intervalDone(vj, i) {
			continue
		}
		ch := make(chan map[string]checkReading, 1)
		out[i] = ch
		start := vj.Start + int64(i)*vj.IntervalMS
		from, to := time.UnixMilli(start), time.UnixMilli(start+vj.IntervalMS)
		go func() {
			if w := time.Until(from); w > 0 {
				select {
				case <-time.After(w):
				case <-ctx.Done():
					return
				}
			}
			got := make(map[string]checkReading, len(checks))
			done := make(chan struct {
				name string
				r    checkReading
			}, len(checks))
			for _, m := range checks {
				go func() {
					res, err := d.Sources.Check(ctx, m.Source, job.Project, m.Check, from, to)
					done <- struct {
						name string
						r    checkReading
					}{m.Name, checkReading{res, err}}
				}()
			}
			for range checks {
				x := <-done
				got[x.name] = x.r
			}
			ch <- got
		}()
	}
	return out
}

func intervalDone(vj *engine.VerifyJob, i int) bool {
	for _, d := range vj.Done {
		if d == i {
			return true
		}
	}
	return false
}

// onlyChecks reports whether every query of the verification is a health
// check, which needs no wait for a source to catch up.
func onlyChecks(vj *engine.VerifyJob) bool {
	if len(vj.Logs) > 0 {
		return false
	}
	for _, m := range vj.Metrics {
		if m.Check == nil {
			return false
		}
	}
	return true
}

// metricFromCheck turns a reading into the interval's report.
func metricFromCheck(name string, r checkReading) verify.MetricData {
	md := verify.MetricData{Name: name}
	if r.err != nil {
		md.Error = r.err.Error()
		return md
	}
	if len(r.res.Attempts) == 0 {
		md.Error = "no call was made in this interval (the deploy runner was stopping, or the interval started late)"
		return md
	}
	md.Canary, md.Attempts, md.Note = r.res.Points, r.res.Attempts, r.res.Note
	return md
}

// settleFor is how long after an interval ends its data are read: none for
// a verification of health checks only, which were made during the interval.
func (d *DeployRunner) settleFor(vj *engine.VerifyJob) time.Duration {
	if onlyChecks(vj) {
		return 0
	}
	if d.Settle != 0 {
		return d.Settle
	}
	return defaultSettle
}

// takeChecks waits for an interval's health checks; ok is false when the
// runner is stopping. An interval without checks gives nil.
func takeChecks(ctx context.Context, ch chan map[string]checkReading) (map[string]checkReading, bool) {
	if ch == nil {
		return nil, true
	}
	select {
	case got := <-ch:
		return got, true
	case <-ctx.Done():
		return nil, false
	}
}
