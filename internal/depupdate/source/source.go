// Package source answers "what versions exist?" for each ecosystem: the Go
// module proxy protocol, the npm registry's package documents, PyPI's JSON
// API and OCI registries' tag lists. Every request goes through one Fetcher,
// which caches, respects the installation's egress rules (the HTTP client it
// is given is the egress guard's) and has an offline mode in which only
// mirrors the installation named are asked.
//
// The built-in package registry and pull-through cache (designed, not built)
// becomes one more Source of an ecosystem: set its address as that source's
// BaseURL; nothing else changes.
package source

import (
	"context"
	"errors"
	"time"
)

// Release is one published version.
type Release struct {
	Version    string    `json:"version"`
	Module     string    `json:"module,omitempty"` // Go: the path of a higher major version ("x/y/v2")
	Time       time.Time `json:"time,omitzero"`    // when it was published; zero when the source does not say
	Yanked     bool      `json:"yanked,omitempty"` // withdrawn: never proposed
	Deprecated string    `json:"deprecated,omitempty"`
	Digest     string    `json:"digest,omitempty"` // container images: the manifest digest of this tag
	NotesURL   string    `json:"notes_url,omitempty"`
}

// Source lists the versions of one ecosystem.
type Source interface {
	Ecosystem() string
	// Versions lists the published versions of a package, in no particular
	// order. Times may be missing; Detail fills them in for one version.
	Versions(ctx context.Context, name string) ([]Release, error)
	// Detail returns one version with its time, digest and notes link.
	Detail(ctx context.Context, name, version string) (Release, error)
}

// Errors.
var (
	// ErrOffline: offline mode is on and no mirror is configured for this source.
	ErrOffline = errors.New("offline mode: no mirror is configured for this source")
	// ErrNotFound: the source does not know the package.
	ErrNotFound = errors.New("the source does not know this package")
)
