package engine

// reconnected runs when the listening connection is back after being lost
// (a failover, a restart of the database, a network break). NOTIFY does not
// survive that: a notification sent while nothing was listening, or just
// before the old primary died, is never delivered. So everything a
// notification could have said is done now, not only the wake-up for queued
// work but the configuration handlers too. The configuration revision poll
// (settings.Service.Poll) stays as the guarantee: no correctness property
// depends on a notification arriving.
func (e *Engine) reconnected() {
	e.notify()        // work may have been queued while nothing was listening
	e.configChanged() // so may a change of configuration
}
