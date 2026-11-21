package pulls

import (
	"context"
	"slices"
	"strings"
)

// openRequests counts the open pull requests of a repository a person has
// been asked to review: their load.
func (s *Store) openRequests(ctx context.Context, repoID, name string) int {
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM pull_people pp JOIN pull_requests pr ON pr.id = pp.pull_id
		WHERE pr.repo_id = ? AND pr.state = 'open' AND pp.kind = 'reviewer' AND pp.name = ?`, repoID, name).Scan(&n)
	return n
}

// AutoAssign asks reviewers for a pull request that is ready for review, by
// the repository's setting: the owners of the changed files, by the way the
// setting chooses among several people. It never asks the author, a person who
// already reviewed, or one already asked, and it does nothing for a draft.
// It returns who was asked (people, then groups with their prefix).
func (s *Service) AutoAssign(ctx context.Context, repoID string, p PullRequest, by string) []string {
	if p.State != Open || p.Draft || IsWIP(p.Title) {
		return nil
	}
	set, err := s.Store.Settings(ctx, repoID)
	if err != nil || set.AssignReviewers == "" {
		return nil
	}
	changed, err := s.ChangedPaths(ctx, repoID, p)
	if err != nil {
		return nil
	}
	view, _, err := s.Owners(ctx, repoID, p, changed)
	if err != nil || !view.Present || len(view.Problems) > 0 {
		return nil
	}
	reviews, _ := s.Store.Reviews(ctx, p.ID)
	done := map[string]bool{p.Author: true}
	for _, r := range reviews {
		if r.State == "submitted" {
			done[r.Author] = true
		}
	}
	for _, r := range p.Reviewers {
		done[r] = true
	}
	var people, groups []string
	picked := map[string]bool{}
	load := map[string]int{}
	for _, f := range view.Files {
		var cands []string
		covered := false
		for _, o := range f.Owners {
			if g, ok := strings.CutPrefix(o, groupPrefix); ok {
				if !slices.Contains(p.ReviewerGroups, g) && !slices.Contains(groups, g) {
					groups = append(groups, g)
				}
				covered = true
				continue
			}
			if picked[o] || (done[o] && o != p.Author) {
				covered = true // one of this file's owners is already involved
				continue
			}
			if o == p.Author {
				continue
			}
			cands = append(cands, o)
		}
		if covered && set.AssignReviewers != AssignOwners {
			continue
		}
		switch set.AssignReviewers {
		case AssignOwners:
			for _, c := range cands {
				picked[c] = true
				people = append(people, c)
			}
		case AssignRoundRobin, AssignLeastLoaded:
			if len(cands) == 0 {
				continue
			}
			pick := cands[p.Number%len(cands)]
			if set.AssignReviewers == AssignLeastLoaded {
				best := -1
				for _, c := range cands {
					if _, ok := load[c]; !ok {
						load[c] = s.Store.openRequests(ctx, repoID, c)
					}
					if best < 0 || load[c] < best {
						best, pick = load[c], c
					}
				}
			}
			picked[pick] = true
			load[pick]++
			people = append(people, pick)
		}
	}
	if len(people) > 0 {
		if err := s.RequestReview(ctx, repoID, p, by, people); err != nil {
			people = nil
		}
	}
	if len(groups) > 0 {
		if err := s.RequestReviewGroups(ctx, repoID, p, by, groups); err != nil {
			groups = nil
		}
	}
	out := slices.Clone(people)
	for _, g := range groups {
		out = append(out, groupPrefix+g)
	}
	return out
}
