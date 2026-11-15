//go:build unix

package executor

import "syscall"

// openNonBlock opens a file without waiting: a pipe opened this way does not
// block until something writes to it.
const openNonBlock = syscall.O_NONBLOCK
