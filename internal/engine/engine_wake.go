package engine

func (e *Engine) waitChan() <-chan struct{} {
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()
	return e.wake
}

// notify wakes every Lease waiting on this server.
func (e *Engine) notify() {
	e.wakeMu.Lock()
	close(e.wake)
	e.wake = make(chan struct{})
	e.wakeMu.Unlock()
}
