package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// ChangedFiles lists the files a push changes, for a push policy that limits
// which files a credential may write. It is given to the policy through its
// context (ChangedFilesFrom) because the objects of a push are still in
// quarantine when the policy runs.
type ChangedFiles func(updates []RefUpdate) ([]string, error)

type changedKey struct{}

// ChangedFilesFrom returns the function a policy may use to list the files
// the push changes, or nil outside a push.
func ChangedFilesFrom(ctx context.Context) ChangedFiles {
	f, _ := ctx.Value(changedKey{}).(ChangedFiles)
	return f
}

// withChangedFiles gives a policy the means to list the files of the push.
// The files are those touched by commits the push brings (commits the
// repository does not have yet), whichever ref they arrive on, so a push that
// adds a commit and reverts it in the next still reports the file.
func (st *pushState) withChangedFiles(ctx context.Context, quarantine string) context.Context {
	return context.WithValue(ctx, changedKey{}, ChangedFiles(func(updates []RefUpdate) ([]string, error) {
		if quarantine == "" {
			return nil, errors.New("the pushed objects are not available to read")
		}
		seen := map[string]bool{}
		var out []string
		for _, u := range updates {
			if realRefUpdates([]RefUpdate{u}) == nil || strings.Trim(u.New, "0") == "" {
				continue // a deletion changes no file
			}
			args := []string{"log", "--format=", "--name-only", "--no-renames", "-m", u.New, "--not", "--all"}
			if strings.Trim(u.Old, "0") != "" {
				args = []string{"log", "--format=", "--name-only", "--no-renames", "-m", u.New, "^" + u.Old}
			}
			dir := st.store.cacheDir(st.repo)
			stdout, err := st.store.gitWith(ctx, dir, []string{
				"GIT_OBJECT_DIRECTORY=" + quarantine,
				"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(dir, "objects"),
			}, args...)
			if err != nil {
				return nil, err
			}
			for _, f := range strings.Split(stdout, "\n") {
				if f = strings.TrimSpace(f); f != "" && !seen[f] {
					seen[f] = true
					out = append(out, f)
				}
			}
		}
		return out, nil
	}))
}
