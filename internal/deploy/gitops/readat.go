package gitops

import "context"

// ReadAtParent returns a file as the parent of commit held it (nil when it did
// not exist there). It changes nothing.
func (r *Repo) ReadAtParent(ctx context.Context, commit, p string) ([]byte, error) {
	if err := checkRevision(commit); err != nil {
		return nil, err
	}
	cp, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	return r.show(ctx, commit+"^", cp)
}

// ReadAtRevision returns a file as a revision (a tag, a commit) holds it, from
// the repository's own history or after fetching that revision; nil when the
// file is not there. It changes nothing.
func (r *Repo) ReadAtRevision(ctx context.Context, rev, p string) ([]byte, error) {
	if err := checkRevision(rev); err != nil {
		return nil, err
	}
	cp, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	if _, err := r.open(ctx); err != nil {
		return nil, err
	}
	if data, err := r.show(ctx, rev, cp); err != nil || data != nil {
		return data, err
	}
	if _, err := r.git(ctx, nil, "fetch", "--quiet", "--no-tags", "origin", rev); err != nil {
		return nil, err
	}
	return r.show(ctx, "FETCH_HEAD", cp)
}

func (r *Repo) show(ctx context.Context, rev, p string) ([]byte, error) {
	code, out, err := r.gitCode(ctx, nil, "show", rev+":"+p)
	if err != nil || code != 0 {
		return nil, nil // the revision or the file is not there
	}
	return []byte(out), nil
}
