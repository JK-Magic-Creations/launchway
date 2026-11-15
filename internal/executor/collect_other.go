//go:build !unix

package executor

// openNonBlock is not needed where there are no named pipes to block on.
const openNonBlock = 0
