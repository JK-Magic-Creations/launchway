package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// PushObject is a commit or annotated tag a push brings, as git stores it.
type PushObject struct {
	SHA  string
	Kind string // commit or tag
	Raw  []byte
}

// NewObjects lists the commits and annotated tags a push brings (those the
// repository does not have yet), each with its raw bytes, for a push policy
// that judges signatures. It is given to the policy through its context
// (NewObjectsFrom) because the objects are still in quarantine when the
// policy runs. more is true when the push brings more than limit commits; the
// first limit are returned.
type NewObjects func(updates []RefUpdate, limit int) (objs []PushObject, more bool, err error)

type newObjectsKey struct{}

// NewObjectsFrom returns the function a policy uses to read the objects of
// the push, or nil outside a push.
func NewObjectsFrom(ctx context.Context) NewObjects {
	f, _ := ctx.Value(newObjectsKey{}).(NewObjects)
	return f
}

func (st *pushState) withNewObjects(ctx context.Context, quarantine string) context.Context {
	return context.WithValue(ctx, newObjectsKey{}, NewObjects(func(updates []RefUpdate, limit int) ([]PushObject, bool, error) {
		if quarantine == "" {
			return nil, false, errors.New("the pushed objects are not available to read")
		}
		dir := st.store.cacheDir(st.repo)
		env := []string{"GIT_OBJECT_DIRECTORY=" + quarantine, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(dir, "objects")}
		seen := map[string]bool{}
		var out []PushObject
		more := false
		read := func(kind, sha string) error {
			raw, err := st.store.gitWith(ctx, dir, env, "cat-file", kind, sha)
			if err != nil {
				return err
			}
			out = append(out, PushObject{SHA: sha, Kind: kind, Raw: []byte(raw)})
			return nil
		}
		for _, u := range updates {
			if realRefUpdates([]RefUpdate{u}) == nil || strings.Trim(u.New, "0") == "" || !validObjectName(u.New) {
				continue
			}
			kind, err := st.store.gitWith(ctx, dir, env, "cat-file", "-t", u.New)
			if err != nil {
				return nil, false, err
			}
			kind = strings.TrimSpace(kind)
			if kind == "tag" && !seen[u.New] {
				seen[u.New] = true
				if err := read("tag", u.New); err != nil {
					return nil, false, err
				}
			}
			if kind != "commit" && kind != "tag" {
				continue
			}
			args := []string{"rev-list", "--limit-count=" + fmt.Sprint(limit+1), u.New, "--not", "--all"}
			if strings.Trim(u.Old, "0") != "" && validObjectName(u.Old) {
				args = []string{"rev-list", "--limit-count=" + fmt.Sprint(limit+1), u.New, "^" + u.Old}
			}
			list, err := st.store.gitWith(ctx, dir, env, args...)
			if err != nil {
				return nil, false, err
			}
			for _, sha := range strings.Fields(list) {
				if seen[sha] {
					continue
				}
				if len(out) >= limit {
					more = true
					break
				}
				seen[sha] = true
				if err := read("commit", sha); err != nil {
					return nil, false, err
				}
			}
		}
		return out, more, nil
	}))
}

func validObjectName(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
