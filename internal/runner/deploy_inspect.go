package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// maxInspections bounds the requests one deploy runner answers at once: a
// followed log holds one for as long as it is watched.
const maxInspections = 24

// inspections takes requests to look into deliveries (the object tree,
// manifests, logs, workload controls) and answers them. Each runs on its
// own, bounded by maxInspections; a request is a read of the cluster, or a
// small change the server already checked a person may make.
func (d *DeployRunner) inspections(ctx context.Context) {
	vl := d.vitals().loop("inspect")
	sem := make(chan struct{}, maxInspections)
	backoff := time.Second
	for ctx.Err() == nil {
		vl.slotWait(true)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		vl.slotWait(false)
		job, err := d.Client.TakeInspect(ctx, d.Info, 20*time.Second)
		vl.polled()
		if err != nil || job == nil {
			<-sem
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			wait := backoff
			if strings.Contains(err.Error(), "no such API endpoint") {
				wait = 5 * time.Minute // a server that does not know inspect requests
			}
			d.Log.Warn("could not ask the server for inspect requests; retrying", "err", err, "in", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		go func() {
			defer func() { <-sem }()
			d.inspect(ctx, job)
		}()
	}
}

func (d *DeployRunner) inspect(ctx context.Context, job *engine.InspectJob) {
	log := d.Log.With("inspect", job.ID, "kind", job.Kind, "deployment", job.Deployment, "actor", job.Actor)
	until := time.UnixMilli(job.Until)
	actx, cancel := context.WithDeadline(ctx, until.Add(-2*time.Second))
	defer cancel()
	chunk := func(body string) (bool, error) {
		cctx, c := context.WithTimeout(ctx, 10*time.Second)
		defer c()
		stop, err := d.Client.InspectChunk(cctx, d.Info, job.ID, body)
		if stop {
			cancel()
		}
		return stop, err
	}
	result, err := d.Agent.Inspect(actx, job, chunk)
	msg := ""
	if err != nil {
		msg = err.Error()
		log.Info("inspect request could not be answered", "err", msg)
	}
	for i := 0; i < 4; i++ {
		fctx, c := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		err := d.Client.FinishInspect(fctx, d.Info, job.ID, result, msg)
		c()
		if err == nil {
			return
		}
		log.Warn("reporting an inspect answer failed; retrying", "err", fmt.Sprint(err))
		time.Sleep(time.Duration(i+1) * time.Second)
	}
}

// RunInspections answers inspect requests only, until ctx ends. A deploy
// runner does this as part of Run; this is for a process that does nothing
// else, and for tests.
func (d *DeployRunner) RunInspections(ctx context.Context) { d.inspections(ctx) }
