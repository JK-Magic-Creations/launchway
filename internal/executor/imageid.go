package executor

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ImageResolver is implemented by executors that can say which image bytes a
// reference is, from where they run it.
type ImageResolver interface {
	// ResolveImage returns the image's content id (a digest or the engine's
	// image id) and where it came from.
	ResolveImage(ctx context.Context, ref string) (id, source string, err error)
}

var digestInRef = regexp.MustCompile(`@(sha256:[0-9a-f]{64})$`)

// resolveTimeout bounds asking an engine or a registry.
const resolveTimeout = 30 * time.Second

// ResolveImage works out which image a step runs in, for its caches' keys.
// A reference that already carries a digest is that digest. Otherwise the
// executor is asked: a container engine reports its own copy (the one the
// step will run, as a missing image is pulled), the Kubernetes executor asks
// the image's registry. If nothing can say, the identity has no ID and the
// key uses the reference as written, which the run's log says.
func ResolveImage(ctx context.Context, ex Executor, ref string) ImageIdentity {
	id := ImageIdentity{Ref: ref}
	if m := digestInRef.FindStringSubmatch(ref); m != nil {
		id.ID, id.Source = m[1], "digest in the reference"
		return id
	}
	if _, ok := ex.(*Host); ok || ref == "" {
		id.Source = "the host executor ignores the image"
		return id
	}
	r, ok := ex.(ImageResolver)
	if !ok {
		return id
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	got, source, err := r.ResolveImage(ctx, ref)
	if err != nil || got == "" {
		return id
	}
	id.ID, id.Source = got, source
	return id
}

// ResolveImage asks the engine for the image id, pulling the image if the
// engine has none (as running the step would).
func (c *Container) ResolveImage(ctx context.Context, ref string) (string, string, error) {
	inspect := func() (string, error) {
		out, err := exec.CommandContext(ctx, c.Bin, "image", "inspect", "--format", "{{.Id}}", ref).Output()
		return strings.TrimSpace(string(out)), err
	}
	id, err := inspect()
	if err != nil || id == "" {
		if perr := exec.CommandContext(ctx, c.Bin, "pull", "--quiet", ref).Run(); perr != nil {
			return "", "", fmt.Errorf("pulling %s: %w", ref, perr)
		}
		if id, err = inspect(); err != nil {
			return "", "", err
		}
	}
	if !strings.Contains(id, ":") {
		id = "sha256:" + id
	}
	return id, "container engine", nil
}

// ResolveImage asks the image's registry for the digest of the reference.
func (k *Kubernetes) ResolveImage(ctx context.Context, ref string) (string, string, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return "", "", err
	}
	d, err := remote.Head(r, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return "", "", err
	}
	return d.Digest.String(), "registry", nil
}
