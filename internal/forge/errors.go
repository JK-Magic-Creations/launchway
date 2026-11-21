package forge

import "errors"

// Errors every provider and the store share, so that a client of the forge
// can tell them apart without importing a provider.
var (
	// ErrNotFound: no such repository (or connection, or event).
	ErrNotFound = errors.New("not found")
	// ErrExists: something with that name is already there.
	ErrExists = errors.New("already exists")
	// ErrQuota: an owner has as many repositories as the server allows.
	ErrQuota = errors.New("this owner has reached the limit of repositories")
	// ErrRateLimited: repositories are being created faster than the server allows.
	ErrRateLimited = errors.New("repositories are being created too fast; try again in a minute")
)
