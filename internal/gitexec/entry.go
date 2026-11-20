package gitexec

import "sync"

var (
	entryMu sync.Mutex
	entries []func()
)

// RegisterEntry adds a function Reexec calls first thing in main, for
// packages whose code runs when the launchway binary is started under another
// name: git's hooks of a hosted repository are symlinks to the binary, and
// the hosting package answers them. Each function returns at once if the
// process was not started for it, and exits the process if it was.
// Registration happens in init, so linking the package is enough.
func RegisterEntry(f func()) {
	entryMu.Lock()
	defer entryMu.Unlock()
	entries = append(entries, f)
}

func runEntries() {
	entryMu.Lock()
	list := append([]func(){}, entries...)
	entryMu.Unlock()
	for _, f := range list {
		f()
	}
}
