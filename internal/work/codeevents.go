package work

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// PullChange says what happened to a pull request, for the work it names.
type PullChange struct {
	Repo    string // owner/name
	Number  int
	Title   string
	Body    string
	Branch  string   // the pull request's own branch
	State   string   // open, merged or closed
	Commits []string // messages of its commits
	// By is who made the change (the person who opened or merged it). A
	// closing keyword moves work only when a person merged.
	By Caller
	// MayClose says whether By may move work of another project; work in the
	// pull request's own project needs no such permission. Closing across
	// projects is never done without it.
	MayClose func(Scope) bool
}

// Applied says what the work did with a change.
type Applied struct {
	Linked   []string `json:"linked"`
	Moved    []string `json:"moved"`
	NotMoved []string `json:"not_moved,omitempty"`
	Unknown  []string `json:"unknown,omitempty"`
}

// ApplyPull links the work a pull request names and, when the rule says so,
// moves it: to In review when the pull request opens, to the rule's state when
// it merges. It never closes work of another project unless the caller may.
func (s *Store) ApplyPull(ctx context.Context, home Scope, ch PullChange) (*Applied, error) {
	out := &Applied{}
	text := ch.Title + "\n" + ch.Body + "\n" + ch.Branch + "\n" + strings.Join(ch.Commits, "\n")
	refs := ParseReferences(text)
	if len(refs) == 0 {
		return out, nil
	}
	eff, err := s.Settings(ctx, home)
	if err != nil {
		return nil, err
	}
	rule := DefaultClose()
	if eff.Close != nil {
		rule = *eff.Close
		if rule.State == "" {
			rule.State = "done"
		}
	}
	sys := System("git")
	seen := map[string]bool{}
	for _, r := range refs {
		it, err := s.ResolveRef(ctx, home, r)
		if err != nil {
			out.Unknown = append(out.Unknown, r.Ref)
			continue
		}
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		closing := r.Closes && (len(rule.Verbs) == 0 || slices.Contains(rule.Verbs, canonicalVerb(r.Verb)))
		if _, err := s.LinkCode(ctx, sys, it.ID, CodeLink{Kind: CodePull, Repo: ch.Repo, Ref: fmt.Sprint(ch.Number), Title: ch.Title, State: ch.State, Closes: closing, Source: "message"}); err != nil {
			return nil, err
		}
		out.Linked = append(out.Linked, it.Ref)
		if !closing || rule.On == "never" {
			continue
		}
		crossProject := it.Scope != home
		if crossProject && (ch.MayClose == nil || !ch.MayClose(it.Scope)) {
			out.NotMoved = append(out.NotMoved, it.Ref+" (another project: you may not close its work)")
			continue
		}
		ieff, err := s.Settings(ctx, it.Scope)
		if err != nil {
			return nil, err
		}
		if ieff.Closed(it.State) {
			continue
		}
		var to string
		switch {
		case ch.State == "open" && ieff.Category(it.State) != CatReview:
			to = ieff.FirstOf(CatReview)
		case ch.State == "merged" && rule.On != "review":
			to = rule.State
			if _, ok := ieff.StateByKey(to); !ok {
				to = ieff.FirstOf(CatDone)
			}
			if !ch.By.Human() {
				// Agents do not finish work, nor does merging by an agent: a
				// person confirms in the review.
				to = ieff.FirstOf(CatReview)
				out.NotMoved = append(out.NotMoved, it.Ref+" (merged by an agent: it waits in review for a person)")
			}
		}
		if to == "" || to == it.State {
			continue
		}
		by := ch.By
		if by.Name == "" {
			by = sys
		}
		by.Via = "git"
		// The move is made as the person who merged, so the history says who.
		by.Write = func(Scope) bool { return true }
		by.Read = func(Scope) bool { return true }
		by.Agent = nil
		if _, err := s.Update(ctx, by, it.ID, Patch{State: &to}); err != nil {
			out.NotMoved = append(out.NotMoved, it.Ref+" ("+err.Error()+")")
			continue
		}
		out.Moved = append(out.Moved, it.Ref)
	}
	return out, nil
}

func canonicalVerb(v string) string {
	switch {
	case strings.HasPrefix(v, "fix"):
		return "fixes"
	case strings.HasPrefix(v, "clos"):
		return "closes"
	case strings.HasPrefix(v, "resolv"):
		return "resolves"
	}
	return v
}

// CommitChange is a commit that reached a branch.
type CommitChange struct {
	Repo     string
	SHA      string
	Message  string
	Branch   string
	Default  bool // the repository's default branch
	By       Caller
	MayClose func(Scope) bool
}

// ApplyCommit links the work a commit message names. A closing keyword on a
// commit that reaches the default branch directly moves the work the same way a
// merge does.
func (s *Store) ApplyCommit(ctx context.Context, home Scope, ch CommitChange) (*Applied, error) {
	out := &Applied{}
	refs := ParseReferences(ch.Message + "\n" + ch.Branch)
	if len(refs) == 0 {
		return out, nil
	}
	sys := System("git")
	seen := map[string]bool{}
	for _, r := range refs {
		it, err := s.ResolveRef(ctx, home, r)
		if err != nil {
			out.Unknown = append(out.Unknown, r.Ref)
			continue
		}
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		title := strings.SplitN(ch.Message, "\n", 2)[0]
		if _, err := s.LinkCode(ctx, sys, it.ID, CodeLink{Kind: CodeCommit, Repo: ch.Repo, Ref: ch.SHA, Title: title, State: map[bool]string{true: "default", false: ch.Branch}[ch.Default], Closes: r.Closes, Source: "message"}); err != nil {
			return nil, err
		}
		out.Linked = append(out.Linked, it.Ref)
	}
	if ch.Default && ch.By.Human() {
		pc := PullChange{Repo: ch.Repo, Title: ch.Message, State: "merged", By: ch.By, MayClose: ch.MayClose}
		pc.Number = 0
		res, err := s.applyClosingOnly(ctx, home, pc)
		if err != nil {
			return nil, err
		}
		out.Moved, out.NotMoved = res.Moved, res.NotMoved
	}
	return out, nil
}

// applyClosingOnly applies a merge's closing rule without recording a pull
// request link (used for a commit straight to the default branch).
func (s *Store) applyClosingOnly(ctx context.Context, home Scope, ch PullChange) (*Applied, error) {
	saved := ch
	saved.Number = -1
	res, err := s.ApplyPull(ctx, home, saved)
	if err != nil {
		return nil, err
	}
	// Remove the placeholder pull link ApplyPull made.
	if _, err := s.db.Exec(ctx, `DELETE FROM work_code_links WHERE kind = 'pull' AND repo = ? AND ref = '-1'`, ch.Repo); err != nil {
		return nil, err
	}
	return res, nil
}
