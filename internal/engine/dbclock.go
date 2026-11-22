package engine

import (
	"context"
	"sync/atomic"
	"time"
)

// Leases are judged on the database's time. Servers sharing a PostgreSQL
// database can disagree about the time by seconds; a lease written by one
// and judged by another would then expire early or late. Each server
// measures how far its clock is from the database's and adds that to
// everything it writes or compares (see syncClock), so that all of them
// agree with the database. SQLite is local and has no offset; tests that
// inject a clock are never adjusted.
type dbClock struct {
	skew     atomic.Int64 // ns to add to the local clock to get the database's
	lastGood atomic.Int64 // real UnixNano of the last successful markAlive; 0 before the first
	real     func() time.Time
}

// newDBClock starts a clock whose last good moment is now: a server whose
// first tick meets an outage must apply the rule as much as one that had
// ticked before.
func newDBClock() *dbClock {
	c := &dbClock{}
	c.lastGood.Store(time.Now().UnixNano())
	return c
}

func (c *dbClock) wrap(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return func() time.Time { return time.Now().Add(time.Duration(c.skew.Load())) }
}

func (c *dbClock) realNow() time.Time {
	if c.real != nil {
		return c.real()
	}
	return time.Now()
}

// syncClock measures this server's clock against the database's (PostgreSQL
// only), and keeps the measurement if the round trip was quick enough to trust.
func (e *Engine) syncClock() {
	if !e.db.pg || e.db.clk == nil {
		return
	}
	before := time.Now()
	var ms int64
	if err := e.db.QueryRow(`SELECT (extract(epoch FROM clock_timestamp()) * 1000)::bigint`).Scan(&ms); err != nil {
		return
	}
	after := time.Now()
	if rtt := after.Sub(before); rtt < 2*time.Second {
		mid := before.Add(rtt / 2)
		e.db.clk.skew.Store(int64(time.UnixMilli(ms).Sub(mid)))
	}
}

// markAlive records that this server is alive. If this server could not
// reach the database for longer than half a lease TTL (a failover, a
// restart of the database, a long stall), the leases that "aged" meanwhile
// are the same case as a server restart: nobody could renew them, so the
// restart rule applies (see recoverLeases): live leases and runners get a
// fresh TTL, and the reaper expires only what stays silent after that.
// alive is what the reaper does each tick to say this server is here: it records it, and looks (every few seconds)
// whether a server of an older release is still connected, which commands read without asking the database.
func (e *Engine) alive(ctx context.Context) {
	e.refreshOlder(ctx)
	e.markAlive()
}

func (e *Engine) markAlive() {
	unlock := e.writeLock()
	defer unlock()
	e.syncClock()
	err := e.db.retryTx(0, true, func() error { return e.markAliveTx() })
	if err != nil {
		e.log.Error("recording that this server is alive", "err", err)
	}
}

func (e *Engine) markAliveTx() error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := e.now()
	clk := e.db.clk
	if last := clk.lastGood.Load(); last != 0 && clk.realNow().Sub(time.Unix(0, last)) > e.ttl/2 {
		until := now.Add(e.ttl).UnixMilli()
		res, err := tx.Exec(`UPDATE leases SET expires_at = ? WHERE expires_at < ?`, until, until)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE runners SET last_seen = ? WHERE last_seen < ? AND last_seen > ?`,
			now.UnixMilli(), now.UnixMilli(), now.Add(-3*e.runnerTimeout).UnixMilli()); err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			e.log.Warn("the database was not available to this server; running steps keep their leases for a fresh TTL", "leases", n)
		}
	}
	if err := setAlive(tx, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	clk.lastGood.Store(clk.realNow().UnixNano())
	return nil
}
