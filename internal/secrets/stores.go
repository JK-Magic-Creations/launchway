package secrets

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Env reads the runner process's environment variables. The key is the
// variable's name.
type Env struct{}

func (Env) Get(_ context.Context, key, field string) (string, error) {
	if field != "" {
		return "", notFound("env stores have no fields; remove field")
	}
	// The runner's own settings, such as its token, are not for pipelines.
	if strings.HasPrefix(strings.ToUpper(key), "LAUNCHWAY_") {
		return "", notFound("variables starting with LAUNCHWAY_ cannot be read as secrets")
	}
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", notFound("the runner has no environment variable %q", key)
	}
	return v, nil
}

// File reads files in a directory, such as a mounted Kubernetes secret. The
// key is a file name relative to Dir; one trailing newline is removed.
type File struct {
	Dir string
}

func (f File) Get(_ context.Context, key, field string) (string, error) {
	if field != "" {
		return "", notFound("file stores have no fields; remove field")
	}
	if !filepath.IsLocal(key) {
		return "", notFound("key %q must be a relative file name inside the store's directory", key)
	}
	// os.Root refuses any path, symbolic links included, that leads
	// outside the directory.
	root, err := os.OpenRoot(f.Dir)
	if err != nil {
		return "", notFound("opening the store's directory: %v", err)
	}
	defer root.Close()
	b, err := root.ReadFile(key)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", notFound("there is no file %q", key)
	case err != nil:
		return "", notFound("reading %q: %v", key, err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}
