package files

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Contributors: who wrote how many commits on a branch over a period. History can be very long, so at most
// MaxContributorCommits commits are walked, newest first; when the walk stops there the answer says AtLeast and the
// counts are lower bounds, never presented as complete. The person is told apart by the author's address (lower case);
// the name shown is the one on that person's newest commit. Merge commits are not counted.

// MaxContributorCommits is the most commits one walk looks at.
const MaxContributorCommits = 50000

// Contributor is one author's row.
type Contributor struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Commits int    `json:"commits"`
	// First and Last are the author times (RFC 3339) of the oldest and newest commit counted.
	First string `json:"first"`
	Last  string `json:"last"`
}

// ContributorReport is the answer for one commit and period.
type ContributorReport struct {
	Commit       string        `json:"commit"`
	Since        string        `json:"since,omitempty"`
	Contributors []Contributor `json:"contributors"`
	// Walked is how many commits were looked at; AtLeast says the walk stopped at the cap with more history left
	// within the period, so every count is "at least".
	Walked  int  `json:"walked"`
	AtLeast bool `json:"at_least"`
}

// Contributors walks the history of ref back to since (zero: all of it), looking at no more than max commits.
func (s *Service) Contributors(ctx context.Context, repoID, ref string, since time.Time, max int) (ContributorReport, error) {
	if max <= 0 || max > MaxContributorCommits {
		max = MaxContributorCommits
	}
	var rep ContributorReport
	err := s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		commit, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		rep = ContributorReport{Commit: commit, Contributors: []Contributor{}}
		args := []string{"log", "--no-merges", "--format=%aN%x1f%aE%x1f%aI%x1e", commit}
		if !since.IsZero() {
			rep.Since = since.UTC().Format(time.RFC3339)
			args = append(args[:2], append([]string{"--since=" + rep.Since}, args[2:]...)...)
		}
		by := map[string]*Contributor{}
		w := &recordWriter{sep: 0x1e, limit: max, each: func(rec string) {
			f := strings.Split(strings.TrimPrefix(rec, "\n"), "\x1f")
			if len(f) < 3 {
				return
			}
			key := strings.ToLower(f[1])
			c := by[key]
			if c == nil { // newest first: the first one seen carries the name to show and the last time
				c = &Contributor{Name: f[0], Email: key, Last: f[2]}
				by[key] = c
			}
			c.Commits++
			c.First = f[2]
		}}
		_, err = gitexec.Run(ctx, gitexec.Spec{GitDir: g.dir, Args: args, Timeout: 5 * time.Minute, Limits: g.limits, Stdout: w})
		if err != nil {
			if !w.stopped {
				return err
			}
			rep.AtLeast = true
		}
		rep.Walked = w.n
		for _, c := range by {
			rep.Contributors = append(rep.Contributors, *c)
		}
		sort.Slice(rep.Contributors, func(i, j int) bool {
			a, b := rep.Contributors[i], rep.Contributors[j]
			if a.Commits != b.Commits {
				return a.Commits > b.Commits
			}
			return a.Email < b.Email
		})
		return nil
	})
	return rep, err
}

// recordWriter hands out the records of a stream, split at sep, and stops it after limit records.
type recordWriter struct {
	buf   []byte
	sep   byte
	limit int
	n     int
	each  func(string)
	// stopped says the limit ended the stream (the command's own error then only says its output was cut).
	stopped bool
}

func (w *recordWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, w.sep)
		if i < 0 {
			return len(b), nil
		}
		rec := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if w.n >= w.limit {
			w.stopped = true
			return 0, errEnough
		}
		w.n++
		w.each(rec)
	}
}
