package engine

import (
	"context"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/metrics"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// metricsEvery is how long the gauges read from the database are kept: a
// scraper that asks every few seconds causes one set of cheap count queries
// per interval, never one per scrape.
const metricsEvery = 10 * time.Second

// RegisterMetrics makes the metrics endpoint refresh the gauges that are read
// from the database (queues, Stations, leases, approvals, mirrors, the
// connection pool) when it is scraped. Every number is a count or an age:
// nothing read here names a repository, a branch or a person.
func (e *Engine) RegisterMetrics(reg *metrics.Registry) {
	var mu sync.Mutex
	var last time.Time
	reg.OnScrape(func() {
		mu.Lock()
		defer mu.Unlock()
		if !last.IsZero() && time.Since(last) < metricsEvery {
			return
		}
		last = time.Now()
		e.collectMetrics()
	})
}

func (e *Engine) collectMetrics() {
	now := time.Now().UnixMilli()
	st := e.db.DB.Stats()
	metrics.DBConnections.Set(float64(st.OpenConnections), "open")
	metrics.DBConnections.Set(float64(st.InUse), "in_use")
	metrics.DBConnections.Set(float64(st.Idle), "idle")
	setTotal(metrics.DBWaits, float64(st.WaitCount))
	setTotal(metrics.DBWaitSeconds, st.WaitDuration.Seconds())

	if info, err := e.Schema(context.Background()); err == nil {
		metrics.SchemaVersion.Set(float64(info.Database))
	}
	if e.knows(schemaPools) {
		fillQueue(store.QueuedStepsByPool(e.db))
	} else {
		fillQueue(store.QueuedStepsAll(e.db))
	}
	metrics.StationsOnline.Reset()
	if rows, err := store.StationsByPool(e.db, now-RunnerOffline.Milliseconds()); err == nil {
		for _, r := range rows {
			metrics.StationsOnline.Set(float64(r.N), r.Pool)
		}
	}
	metrics.LeasesActive.Reset()
	if rows, err := store.LeasesByPool(e.db); err == nil {
		for _, r := range rows {
			metrics.LeasesActive.Set(float64(r.N), r.Pool)
		}
	}
	if n, oldest, err := store.ApprovalsWaiting(e.db); err == nil {
		metrics.ApprovalsWaiting.Set(float64(n))
		age := 0.0
		if oldest > 0 && now > oldest {
			age = float64(now-oldest) / 1000
		}
		metrics.ApprovalOldest.Set(age)
	}
	if lag, err := store.MirrorLagMS(e.db, now); err == nil {
		metrics.MirrorLag.Set(float64(lag) / 1000)
	}
}

func fillQueue(rows []store.PoolCount, err error) {
	if err != nil {
		return
	}
	metrics.QueueDepth.Reset()
	metrics.QueueOldest.Reset()
	now := time.Now().UnixMilli()
	for _, r := range rows {
		metrics.QueueDepth.Set(float64(r.N), r.Pool)
		age := 0.0
		if r.OldestMS > 0 && now > r.OldestMS {
			age = float64(now-r.OldestMS) / 1000
		}
		metrics.QueueOldest.Set(age, r.Pool)
	}
}

// setTotal sets a counter to a total the database/sql pool keeps itself.
func setTotal(v *metrics.Vec, total float64) { v.Set(total) }
