package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// MaxUploadFiles is how many files one upload commit may hold.
const MaxUploadFiles = 100

// MaxUploadBytes is the most one upload commit may carry in all.
const MaxUploadBytes = 8 << 20

// Upload is several files added in one commit.
type Upload struct {
	Branch  string
	Message string
	Files   []UploadFile
	// Replace lets a file overwrite one that exists; without it an existing file is a conflict and nothing is written.
	Replace bool
	Actor   string
}

// UploadFile is one file of an upload: a path inside the repository and its bytes.
type UploadFile struct {
	Path    string
	Content []byte
}

// Uploaded says what an upload did.
type Uploaded struct {
	Branch string   `json:"branch"`
	Paths  []string `json:"paths"`
	Commit string   `json:"commit"`
	Parent string   `json:"parent"`
}

// PutFiles makes one commit on an existing branch that adds the files, in the same way Put adds one: the caller
// checks authorization and the branch rules through check, and the update is a compare-and-swap on the branch.
func (s *Service) PutFiles(ctx context.Context, repoID string, u Upload, check func(ref, old, next string) error) (Uploaded, error) {
	switch {
	case len(u.Files) == 0:
		return Uploaded{}, invalid("an upload needs at least one file")
	case len(u.Files) > MaxUploadFiles:
		return Uploaded{}, invalid("an upload may hold at most %d files", MaxUploadFiles)
	case strings.TrimSpace(u.Message) == "" || len(u.Message) > MaxMessage:
		return Uploaded{}, invalid("a commit needs a message of at most %d bytes", MaxMessage)
	case checkRef(u.Branch) != nil || strings.HasPrefix(u.Branch, "refs/"):
		return Uploaded{}, invalid("%q is not a branch name", u.Branch)
	}
	paths := make([]string, len(u.Files))
	seen := map[string]bool{}
	total := 0
	for i, f := range u.Files {
		p, err := CleanPath(f.Path)
		if err != nil || p == "" {
			return Uploaded{}, invalid("%q is not a path inside the repository", f.Path)
		}
		if seen[p] {
			return Uploaded{}, invalid("%s is in the upload twice", p)
		}
		if len(f.Content) > MaxFileBytes {
			return Uploaded{}, invalid("%s is larger than %d MiB: a file uploaded here may be at most that", p, MaxFileBytes>>20)
		}
		total += len(f.Content)
		seen[p], paths[i] = true, p
	}
	if total > MaxUploadBytes {
		return Uploaded{}, invalid("an upload may carry at most %d MiB in all", MaxUploadBytes>>20)
	}
	var out Uploaded
	_, err := s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		ref := "refs/heads/" + u.Branch
		tip := refs[ref]
		if tip == "" {
			return nil, missing("the branch %s does not exist", u.Branch)
		}
		g := runner{dir: scratch, limits: s.Git.Limits(), scratch: true}
		var clash []string
		for _, p := range paths {
			if cur, err := g.run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", tip+":"+p); err == nil && strings.TrimSpace(cur) != "" {
				if kind, _ := g.run(ctx, nil, "cat-file", "-t", "--end-of-options", tip+":"+p); strings.TrimSpace(kind) != "blob" {
					return nil, fmt.Errorf("%w: %s is a directory", ErrConflict, p)
				}
				clash = append(clash, p)
			}
		}
		if len(clash) > 0 && !u.Replace {
			return nil, fmt.Errorf("%w: %s exist%s already; nothing was uploaded. Send replace to overwrite them", ErrConflict, strings.Join(clash, ", "), map[bool]string{true: "s", false: ""}[len(clash) == 1])
		}
		idx := filepath.Join(scratch, "lw-files-index")
		defer os.Remove(idx)
		g.env = []string{"GIT_INDEX_FILE=" + idx}
		if _, err := g.run(ctx, nil, "read-tree", tip); err != nil {
			return nil, err
		}
		for i, f := range u.Files {
			b, err := g.run(ctx, f.Content, "hash-object", "-w", "--stdin")
			if err != nil {
				return nil, err
			}
			mode := "100644"
			if ls, err := g.run(ctx, nil, "ls-tree", "--end-of-options", tip, "--", paths[i]); err == nil {
				if fl := strings.Fields(ls); len(fl) > 0 && fl[0] == "100755" {
					mode = fl[0]
				}
			}
			if _, err := g.run(ctx, nil, "update-index", "--add", "--cacheinfo", mode+","+strings.TrimSpace(b)+","+paths[i]); err != nil {
				return nil, fmt.Errorf("%w: %s cannot be placed there (a file stands where a directory is needed, or the reverse)", ErrConflict, paths[i])
			}
		}
		tree, err := g.run(ctx, nil, "write-tree")
		if err != nil {
			return nil, err
		}
		g.env = identEnv(u.Actor, s.now())
		commit, err := g.run(ctx, nil, "commit-tree", strings.TrimSpace(tree), "-p", tip, "-m", strings.TrimRight(u.Message, "\n")+"\n")
		if err != nil {
			return nil, err
		}
		commit = strings.TrimSpace(commit)
		if check != nil {
			if err := check(ref, tip, commit); err != nil {
				return nil, err
			}
		}
		out = Uploaded{Branch: u.Branch, Paths: paths, Commit: commit, Parent: tip}
		return []storage.RefUpdate{{Name: ref, Old: tip, New: commit}}, nil
	})
	return out, err
}
