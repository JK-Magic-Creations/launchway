package runner

import "time"

// SetResultWithin shortens how long delivering a finished step's result may take, for the test of a step that
// outlives that time while it runs.
func SetResultWithin(d time.Duration) (restore func()) {
	old := resultWithin
	resultWithin = d
	return func() { resultWithin = old }
}
