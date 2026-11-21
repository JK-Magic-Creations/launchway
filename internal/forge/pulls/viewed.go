package pulls

import (
	"context"
	"strings"
)

// Files marked viewed. A mark is a person's, on one file of one pull request,
// and remembers the file's content (its blob) at the head when it was made, so
// a file that changes after is no longer viewed: the person sees it again.

const maxViewedPaths = 1000

// blobsAt returns the blob of each path at a commit ("-" for a path that is not
// there: a deleted file is its own state to have viewed).
func (g gitRunner) blobsAt(ctx context.Context, commit string, paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		out[p] = "-"
	}
	if len(paths) == 0 {
		return out, nil
	}
	g.env = append(g.env, "GIT_LITERAL_PATHSPECS=1")
	args := append([]string{"ls-tree", "-z", commit, "--"}, paths...)
	res, err := g.raw(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	for _, ent := range strings.Split(res, "\x00") {
		meta, name, ok := strings.Cut(ent, "\t")
		f := strings.Fields(meta)
		if ok && len(f) == 3 && f[1] == "blob" {
			out[name] = f[2]
		}
	}
	return out, nil
}

// Viewed lists the files of a pull request that a person has marked viewed and
// that have not changed since.
func (s *Service) Viewed(ctx context.Context, repoID string, p PullRequest, person string) ([]string, error) {
	rows, err := s.Store.db.Query(ctx, `SELECT path, blob FROM pull_viewed WHERE pull_id = ? AND person = ? ORDER BY path`, p.ID, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	marks := map[string]string{}
	var paths []string
	for rows.Next() {
		var path, blob string
		if err := rows.Scan(&path, &blob); err != nil {
			return nil, err
		}
		marks[path] = blob
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []string{}
	if len(paths) == 0 {
		return out, nil
	}
	var now map[string]string
	if err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		now, err = g.blobsAt(ctx, p.HeadSHA, paths)
		return err
	}); err != nil {
		return nil, err
	}
	for _, path := range paths {
		if marks[path] == now[path] {
			out = append(out, path)
		}
	}
	return out, nil
}

// SetViewed marks files viewed (on) or not (off) for a person.
func (s *Service) SetViewed(ctx context.Context, repoID string, p PullRequest, person string, paths []string, on bool) error {
	if len(paths) == 0 || len(paths) > maxViewedPaths {
		return invalid("name 1 to %d files", maxViewedPaths)
	}
	for _, path := range paths {
		if err := validPath(path); err != nil {
			return err
		}
	}
	if !on {
		for _, path := range paths {
			if _, err := s.Store.db.Exec(ctx, `DELETE FROM pull_viewed WHERE pull_id = ? AND person = ? AND path = ?`, p.ID, person, path); err != nil {
				return err
			}
		}
		return nil
	}
	var blobs map[string]string
	if err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		blobs, err = g.blobsAt(ctx, p.HeadSHA, paths)
		return err
	}); err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := s.Store.db.Exec(ctx, `INSERT INTO pull_viewed(pull_id, person, path, blob, viewed_at) VALUES(?,?,?,?,?)
			ON CONFLICT(pull_id, person, path) DO UPDATE SET blob = excluded.blob, viewed_at = excluded.viewed_at`, p.ID, person, path, blobs[path], ms(s.now())); err != nil {
			return err
		}
	}
	return nil
}
