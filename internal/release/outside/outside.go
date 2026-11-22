package outside

import (
	"context"
	"errors"
	"io"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Target is the outside Git host's repository to publish on: the push
// mirror's address and its credential reference.
type Target struct {
	// URL is the mirror's address, an https (or http) address of a Git
	// repository, with no user name or password in it.
	URL string
	// API is the address of the host's REST API when it cannot be derived
	// from URL (a self-hosted host, a test); empty derives it.
	API string
	// Username goes with the credential on the mirror; a token is not sent
	// with it by the REST implementation.
	Username string
	// Credential is a reference. It is resolved at the moment of a call,
	// never stored with a result and never logged.
	Credential forge.SecretRef
}

// File is one file of a release: opened anew for every read, and streamed.
type File struct {
	Name string
	Size int64
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// Release is what is published: the tag it is for, its title and notes
// (Markdown, sent as they are), whether it is a prerelease, and its files.
type Release struct {
	Tag        string
	Title      string
	Notes      string
	Prerelease bool
	Files      []File
}

// Result says where the release is on the outside host.
type Result struct {
	// URL is the address of the release's page there.
	URL string
}

// Publisher makes a release on an outside Git host for a tag that exists
// there. It is safe to call again for the same release: what is there is
// kept and what is missing is added. Errors never hold the credential.
type Publisher interface {
	Publish(ctx context.Context, t Target, r Release) (Result, error)
}

// ErrTagMissing is wrapped by the error of a publish whose tag the outside
// host does not have (yet): the mirror has not copied it. The caller waits and
// tries again.
var ErrTagMissing = errors.New("the outside host does not have the tag yet")
