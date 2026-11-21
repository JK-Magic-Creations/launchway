package pulls

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

const (
	maxComment   = 64 * 1024
	maxRangeSize = 500
)

// NewComment is a comment to add: of the conversation (no Path), or anchored
// to a line or range of a file.
type NewComment struct {
	Body string
	Path string
	// Line is the last (or only) line, StartLine the first of a range (0: one line).
	Line      int
	StartLine int
	// Side is "new" (default) or "old".
	Side string
	// Commit is the commit the lines are numbered in (default: the head).
	Commit string
	// ReplyTo is the ID of a review comment to answer; Path and the lines are then its own.
	ReplyTo string
}

// ParseSuggestion finds the first suggestion block of a Markdown comment: a
// fenced block opened with ```suggestion. ok is false when there is none.
func ParseSuggestion(body string) (text string, ok bool) {
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \r") != "```suggestion" {
			continue
		}
		var out []string
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimRight(lines[j], " \r") == "```" {
				return strings.Join(out, "\n"), true
			}
			out = append(out, strings.TrimSuffix(lines[j], "\r"))
		}
		return "", false // never closed: not a suggestion
	}
	return "", false
}

func validPath(p string) error {
	if p == "" || len(p) > 4096 || strings.ContainsAny(p, "\x00\n\r") || strings.HasPrefix(p, "/") || p != path.Clean(p) || p == ".." || strings.HasPrefix(p, "../") {
		return invalid("%q is not a path inside the repository", p)
	}
	return nil
}

// readFile returns a file's content at a commit, and whether it exists.
func (g gitRunner) readFile(ctx context.Context, commit, p string) (string, bool, error) {
	if err := validPath(p); err != nil {
		return "", false, err
	}
	typ, err := g.out(ctx, "cat-file", "-t", commit+":"+p)
	if err != nil || typ != "blob" {
		return "", false, nil
	}
	out, err := g.raw(ctx, "", "cat-file", "blob", commit+":"+p)
	return out, err == nil, err
}

// CommentOn adds a comment to the conversation, or a review comment on a line.
func (s *Service) CommentOn(ctx context.Context, repoID string, p PullRequest, author string, n NewComment, reviewID string) (Comment, error) {
	n.Body = strings.TrimRight(n.Body, " \t\r\n")
	switch {
	case strings.TrimSpace(n.Body) == "":
		return Comment{}, invalid("write something before sending a comment")
	case len(n.Body) > maxComment:
		return Comment{}, invalid("the comment is %d KiB, more than the %d KiB allowed", len(n.Body)/1024, maxComment/1024)
	}
	c := Comment{Author: author, Body: n.Body, ReviewID: reviewID}
	switch {
	case n.ReplyTo != "":
		root, err := s.Store.Comment(ctx, p.ID, n.ReplyTo)
		if err != nil || root.Path == "" {
			return Comment{}, invalid("there is no review comment %q to reply to", n.ReplyTo)
		}
		c.ThreadID, c.Path, c.Side, c.Line, c.StartLine, c.OrigLine, c.CommitSHA, c.Outdated = root.ThreadID, root.Path, root.Side, root.Line, root.StartLine, root.OrigLine, root.CommitSHA, root.Outdated
	case n.Path != "":
		if err := s.anchor(ctx, repoID, p, &c, n); err != nil {
			return Comment{}, err
		}
		if sug, ok := ParseSuggestion(n.Body); ok {
			if c.Side != "new" {
				return Comment{}, invalid("a suggestion replaces lines of the changed file; put it on a line of the new side")
			}
			c.Suggestion, c.HasSuggestion = sug, true
		}
	}
	got, err := s.Store.AddComment(ctx, p.ID, c)
	if err != nil {
		return got, err
	}
	detail := map[string]string{"comment": got.ID}
	if m := Mentions(n.Body); len(m) > 0 {
		detail["mentions"] = strings.Join(m, ",")
	}
	s.emit(ctx, "commented", repoID, p, author, detail)
	return got, nil
}

// anchor checks that a review comment points at a real line and fills c.
func (s *Service) anchor(ctx context.Context, repoID string, p PullRequest, c *Comment, n NewComment) error {
	if err := validPath(n.Path); err != nil {
		return err
	}
	side := n.Side
	if side == "" {
		side = "new"
	}
	if side != "new" && side != "old" {
		return invalid("the side of a comment is new or old")
	}
	commit := n.Commit
	if commit == "" {
		commit = p.HeadSHA
	}
	if commit != p.HeadSHA {
		revs, err := s.Store.Revisions(ctx, p.ID)
		if err != nil {
			return err
		}
		known := false
		for _, r := range revs {
			known = known || r.HeadSHA == commit
		}
		if !known {
			return invalid("%s is not a commit this pull request had as its head", commit)
		}
	}
	line, start := n.Line, n.StartLine
	if start == 0 {
		start = line
	}
	switch {
	case line < 1:
		return invalid("name the line to comment on (lines start at 1)")
	case start > line:
		return invalid("a range starts at its first line: %d is after %d", start, line)
	case line-start+1 > maxRangeSize:
		return invalid("a comment covers at most %d lines", maxRangeSize)
	}
	at := commit
	if side == "old" {
		at = p.MergeBase
		if at == "" {
			at = p.BaseSHA
		}
	}
	var content string
	var found bool
	if err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		content, found, err = g.readFile(ctx, at, n.Path)
		return err
	}); err != nil {
		return err
	}
	if !found {
		return invalid("%s does not exist in %s", n.Path, map[string]string{"new": "the pull request's head", "old": "the file before the change"}[side])
	}
	if total := lineCount(content); line > total {
		return invalid("%s has %d lines; line %d does not exist", n.Path, total, line)
	}
	c.Path, c.Side, c.Line, c.StartLine, c.OrigLine, c.CommitSHA = n.Path, side, line, start, line, commit
	if side == "old" {
		c.CommitSHA = at
	}
	return nil
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// ReviewInput is a review to submit.
type ReviewInput struct {
	Verdict  string
	Body     string
	Comments []NewComment
	// Role is the reviewer's role on the repository (the API fills it in).
	Role string
	// Groups are the reviewer's groups (the API fills them in).
	Groups []string
}

// SubmitReview records a verdict on the pull request as it is now, with the
// review's comments.
func (s *Service) SubmitReview(ctx context.Context, repoID string, p PullRequest, author string, in ReviewInput) (Review, error) {
	switch in.Verdict {
	case VerdictComment, VerdictApprove, VerdictRequestChanges:
	default:
		return Review{}, invalid("a review's verdict is comment, approve or request_changes")
	}
	if p.State != Open && in.Verdict != VerdictComment {
		return Review{}, conflict("this pull request is %s; only comments can be added now", p.State)
	}
	if in.Verdict == VerdictComment && strings.TrimSpace(in.Body) == "" && len(in.Comments) == 0 {
		return Review{}, invalid("a review that only comments needs a comment or a summary")
	}
	if len(in.Body) > maxComment {
		return Review{}, invalid("the summary is more than %d KiB", maxComment/1024)
	}
	// The comments are checked before anything is stored: a review is all or nothing.
	for i, c := range in.Comments {
		probe := Comment{}
		if c.Path == "" || c.ReplyTo != "" {
			continue
		}
		if err := s.anchor(ctx, repoID, p, &probe, c); err != nil {
			return Review{}, fmt.Errorf("comment %d: %w", i+1, err)
		}
	}
	r, err := s.Store.AddReview(ctx, p.ID, Review{Author: author, Verdict: in.Verdict, Body: in.Body, CommitSHA: p.HeadSHA, Role: in.Role, Groups: in.Groups})
	if err != nil {
		return r, err
	}
	for _, c := range in.Comments {
		if _, err := s.CommentOn(ctx, repoID, p, author, c, r.ID); err != nil {
			return r, err
		}
	}
	// A person who reviewed is no longer waiting to be asked to.
	if in.Verdict != VerdictComment {
		_ = s.Store.RemovePerson(ctx, p.ID, "reviewer", author)
		for _, g := range in.Groups {
			_ = s.Store.RemovePerson(ctx, p.ID, "reviewer_group", g)
		}
	}
	np, _ := s.Store.ByID(ctx, p.ID)
	detail := map[string]string{"review": r.ID, "verdict": r.Verdict}
	if m := Mentions(in.Body); len(m) > 0 {
		detail["mentions"] = strings.Join(m, ",")
	}
	s.emit(ctx, "review_submitted", repoID, np, author, detail)
	s.afterReview(ctx, repoID, np)
	return r, nil
}

// Dismiss dismisses a review (an approval that no longer holds, or a request
// for changes that has been dealt with), with a reason.
func (s *Service) Dismiss(ctx context.Context, repoID string, p PullRequest, reviewID, by, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return invalid("say why the review is dismissed")
	}
	if err := s.Store.DismissReview(ctx, p.ID, reviewID, by, reason); err != nil {
		return err
	}
	s.emit(ctx, "review_dismissed", repoID, p, by, map[string]string{"review": reviewID, "reason": reason})
	s.afterReview(ctx, repoID, p)
	return nil
}

// RequestReview asks people to review.
func (s *Service) RequestReview(ctx context.Context, repoID string, p PullRequest, by string, names []string) error {
	if p.State != Open {
		return conflict("this pull request is %s", p.State)
	}
	for _, n := range names {
		if n == p.Author {
			return invalid("%s opened this pull request and cannot be asked to review it", n)
		}
		if err := s.Store.AddPerson(ctx, p.ID, "reviewer", n, by); err != nil {
			return err
		}
	}
	np, _ := s.Store.ByID(ctx, p.ID)
	s.emit(ctx, "review_requested", repoID, np, by, map[string]string{"reviewers": strings.Join(names, ",")})
	return nil
}

// RequestReviewGroups asks groups to review: a review by any member answers
// the request. A group name is the sign-in provider's or a local one.
func (s *Service) RequestReviewGroups(ctx context.Context, repoID string, p PullRequest, by string, groups []string) error {
	if p.State != Open {
		return conflict("this pull request is %s", p.State)
	}
	for _, g := range groups {
		g = strings.TrimSpace(g)
		if g == "" || len(g) > 100 || strings.ContainsAny(g, " \t\n\x00,") {
			return invalid("%q is not a group name", g)
		}
		if err := s.Store.AddPerson(ctx, p.ID, "reviewer_group", g, by); err != nil {
			return err
		}
	}
	np, _ := s.Store.ByID(ctx, p.ID)
	s.emit(ctx, "review_requested", repoID, np, by, map[string]string{"groups": strings.Join(groups, ",")})
	return nil
}

// RemoveReviewGroupRequest withdraws a request for a group.
func (s *Service) RemoveReviewGroupRequest(ctx context.Context, repoID string, p PullRequest, name string) error {
	if p.State != Open {
		return conflict("this pull request is %s", p.State)
	}
	if !slices.Contains(p.ReviewerGroups, name) {
		return fmt.Errorf("%w: the group %s has not been asked to review this pull request", ErrNotFound, name)
	}
	return s.Store.RemovePerson(ctx, p.ID, "reviewer_group", name)
}

// RemoveReviewRequest takes a person off the list of those asked to review. It
// changes nothing else: a review the person already gave stays.
func (s *Service) RemoveReviewRequest(ctx context.Context, repoID string, p PullRequest, by, name string) error {
	if p.State != Open {
		return conflict("this pull request is %s", p.State)
	}
	if !slices.Contains(p.Reviewers, name) {
		return fmt.Errorf("%w: %s has not been asked to review this pull request", ErrNotFound, name)
	}
	return s.Store.RemovePerson(ctx, p.ID, "reviewer", name)
}

// dismissStale dismisses approvals of an older head when the repository's
// setting says so; a request for changes stays until its author looks again.
func (s *Service) dismissStale(ctx context.Context, repoID string, p PullRequest, head string) {
	set, err := s.Store.Settings(ctx, repoID)
	if err != nil {
		return
	}
	if !set.DismissStale {
		rules, _ := s.Store.Rules(ctx, repoID)
		if rule, ok := RuleFor(rules, p.TargetBranch); !ok || !rule.DismissStale {
			return
		}
	}
	rs, err := s.Store.Reviews(ctx, p.ID)
	if err != nil {
		return
	}
	for _, r := range rs {
		if r.State == "submitted" && r.Verdict == VerdictApprove && r.CommitSHA != head {
			if err := s.Store.DismissReview(ctx, p.ID, r.ID, "system", "New commits were pushed after this approval."); err == nil {
				s.emit(ctx, "review_dismissed", repoID, p, "system", map[string]string{"review": r.ID, "reason": "new commits"})
			}
		}
	}
}

// ResolveThread marks a thread of review comments resolved or open again.
func (s *Service) ResolveThread(ctx context.Context, repoID string, p PullRequest, thread, by string, on bool) error {
	if err := s.Store.Resolve(ctx, p.ID, thread, by, on); err != nil {
		return err
	}
	s.afterReview(ctx, repoID, p)
	return nil
}

// hunk is one change of a zero-context diff.
type hunk struct{ oldStart, oldLen, newStart, newLen int }

func parseHunks(diff string) []hunk {
	var hs []hunk
	for _, l := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(l, "@@ -") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		o, n := f[1], f[2]
		var h hunk
		h.oldStart, h.oldLen = rangeOf(strings.TrimPrefix(o, "-"))
		h.newStart, h.newLen = rangeOf(strings.TrimPrefix(n, "+"))
		hs = append(hs, h)
	}
	return hs
}

func rangeOf(s string) (start, n int) {
	a, b, ok := strings.Cut(s, ",")
	start, _ = strconv.Atoi(a)
	n = 1
	if ok {
		n, _ = strconv.Atoi(b)
	}
	return
}

// mapLine says where a line of the old file is in the new file, or false when
// the line itself was changed or removed.
func mapLine(hs []hunk, line int) (int, bool) {
	delta := 0
	for _, h := range hs {
		switch {
		case h.oldLen > 0 && line >= h.oldStart && line < h.oldStart+h.oldLen:
			return 0, false
		case h.oldLen == 0 && line <= h.oldStart:
			return line + delta, true // inserted after this line (or before it): later hunks come after
		case line < h.oldStart:
			return line + delta, true
		}
		delta += h.newLen - h.oldLen
	}
	return line + delta, true
}

// reanchorComments moves the comments on the new side of a file along with
// the code when the head moves; a comment whose lines were changed is marked
// outdated and keeps the place it was written.
func (s *Service) reanchorComments(ctx context.Context, repoID string, p PullRequest, head string) {
	cs, err := s.Store.Comments(ctx, p.ID)
	if err != nil {
		return
	}
	_ = s.read(ctx, repoID, func(g gitRunner) error {
		g.env = []string{"GIT_LITERAL_PATHSPECS=1"}
		diffs := map[string][]hunk{}
		bad := map[string]bool{}
		for _, c := range cs {
			if c.Path == "" || c.Side != "new" || c.Outdated || c.CommitSHA == head || c.Deleted {
				continue
			}
			key := c.CommitSHA + "\x00" + c.Path
			if _, done := diffs[key]; !done && !bad[key] {
				out, err := g.raw(ctx, "", "diff", "-U0", "--no-color", "--no-ext-diff", "--no-textconv", c.CommitSHA, head, "--", c.Path)
				if err != nil {
					bad[key] = true
				}
				diffs[key] = parseHunks(out)
			}
			if bad[key] {
				_ = s.Store.Reanchor(ctx, c.ID, c.Line, c.StartLine, c.CommitSHA, true)
				continue
			}
			hs := diffs[key]
			line, ok1 := mapLine(hs, c.Line)
			start, ok2 := mapLine(hs, c.StartLine)
			if !ok1 || !ok2 {
				_ = s.Store.Reanchor(ctx, c.ID, c.Line, c.StartLine, c.CommitSHA, true)
				continue
			}
			_ = s.Store.Reanchor(ctx, c.ID, line, start, head, false)
		}
		return nil
	})
}

// ApplySuggestion applies one suggestion; see ApplySuggestions.
func (s *Service) ApplySuggestion(ctx context.Context, repoID string, p PullRequest, commentID, by string) (string, error) {
	return s.ApplySuggestions(ctx, repoID, p, []string{commentID}, by)
}

// ApplySuggestions applies suggestions of review comments as one commit on the
// pull request's source branch, by someone with push access (the caller
// checks that, and the branch's rules). Every suggestion must still be
// anchored to the current head; two that change the same lines of a file are
// refused, naming both, because only a person can say which wins. The commit
// is made through the ledger's compare-and-swap: a push to the branch in the
// meantime makes it fail, never overwrite. All or none are applied.
func (s *Service) ApplySuggestions(ctx context.Context, repoID string, p PullRequest, commentIDs []string, by string) (string, error) {
	if len(commentIDs) == 0 {
		return "", invalid("name at least one suggestion to apply")
	}
	if len(commentIDs) > 100 {
		return "", invalid("at most 100 suggestions in one commit")
	}
	if p.State != Open {
		return "", conflict("this pull request is %s", p.State)
	}
	if p.SourceRepo != "" {
		return "", conflict("suggestions cannot be applied to a branch of another repository")
	}
	cs, byFile, err := s.suggestionsToApply(ctx, p, commentIDs)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(byFile))
	for path := range byFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	ref := branchRef(p.SourceBranch)
	var made string
	_, err = s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		tip := refs[ref]
		if tip != p.HeadSHA {
			return nil, conflict("the branch %s moved since the pull request last looked; refresh and try again", p.SourceBranch)
		}
		g := s.runner(scratch, true)
		g.env = []string{"GIT_LITERAL_PATHSPECS=1"}
		idx := scratch + "/suggest-index"
		g.env = append(g.env, "GIT_INDEX_FILE="+idx)
		if _, err := g.out(ctx, "read-tree", tip); err != nil {
			return nil, err
		}
		for _, path := range paths {
			content, found, err := g.readFile(ctx, tip, path)
			if err != nil || !found {
				return nil, conflict("%s no longer exists on the branch", path)
			}
			fileCs := byFile[path]
			sort.Slice(fileCs, func(i, j int) bool { return fileCs[i].StartLine > fileCs[j].StartLine }) // from the bottom up, so line numbers hold
			for _, c := range fileCs {
				if content, err = replaceLines(content, c.StartLine, c.Line, c.Suggestion); err != nil {
					return nil, err
				}
			}
			blob, err := g.raw(ctx, content, "hash-object", "-w", "--stdin")
			if err != nil {
				return nil, err
			}
			ent, err := g.out(ctx, "ls-tree", tip, "--", path)
			if err != nil {
				return nil, err
			}
			mode, _, _ := strings.Cut(ent, " ")
			if mode == "" {
				mode = "100644"
			}
			if _, err := g.raw(ctx, mode+" "+strings.TrimSpace(blob)+"\t"+path+"\n", "update-index", "--index-info"); err != nil {
				return nil, err
			}
		}
		tree, err := g.out(ctx, "write-tree")
		if err != nil {
			return nil, err
		}
		g.env = ident(by, s.now())
		var msg string
		if len(cs) == 1 {
			msg = fmt.Sprintf("Apply suggestion from @%s\n\nSuggested in pull request #%d on %s.", cs[0].Author, p.Number, cs[0].Path)
		} else {
			var b strings.Builder
			fmt.Fprintf(&b, "Apply %d suggestions\n\nSuggested in pull request #%d:\n", len(cs), p.Number)
			for _, c := range cs {
				fmt.Fprintf(&b, "- @%s on %s line %d\n", c.Author, c.Path, c.Line)
			}
			msg = b.String()
		}
		made, err = g.out(ctx, "commit-tree", tree, "-p", tip, "-m", msg)
		if err != nil {
			return nil, err
		}
		return []storage.RefUpdate{{Name: ref, Old: tip, New: made}}, nil
	})
	if err != nil {
		return "", err
	}
	for _, c := range cs {
		if err := s.Store.MarkApplied(ctx, p.ID, c.ID, made, by); err != nil {
			return made, err
		}
	}
	s.Pushed(ctx, repoID, by, []RefUpdate{{Name: ref, Old: p.HeadSHA, New: made}})
	return made, nil
}

// suggestionsToApply reads and checks the suggestions named: each must still hold, and no two may
// change the same lines of a file. It returns them, and by file.
func (s *Service) suggestionsToApply(ctx context.Context, p PullRequest, commentIDs []string) ([]Comment, map[string][]Comment, error) {
	var cs []Comment
	seen := map[string]bool{}
	for _, id := range commentIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		c, err := s.Store.Comment(ctx, p.ID, id)
		switch {
		case err != nil:
			return nil, nil, err
		case !c.HasSuggestion || c.Deleted:
			return nil, nil, invalid("the comment on %s line %d holds no suggestion", c.Path, c.Line)
		case c.AppliedSHA != "":
			return nil, nil, conflict("the suggestion on %s line %d was already applied in %s", c.Path, c.Line, c.AppliedSHA[:10])
		case c.Outdated || c.CommitSHA != p.HeadSHA:
			return nil, nil, conflict("the code under the suggestion on %s line %d has changed since it was written; ask for it again on the current code", c.Path, c.Line)
		}
		if c.StartLine == 0 {
			c.StartLine = c.Line
		}
		cs = append(cs, c)
	}
	byFile := map[string][]Comment{}
	for _, c := range cs {
		for _, o := range byFile[c.Path] {
			if c.StartLine <= o.Line && o.StartLine <= c.Line {
				return nil, nil, conflict("the suggestions by @%s and @%s on %s both change lines %d to %d; apply one, then ask for the other again",
					o.Author, c.Author, c.Path, max(c.StartLine, o.StartLine), min(c.Line, o.Line))
			}
		}
		byFile[c.Path] = append(byFile[c.Path], c)
	}
	return cs, byFile, nil
}

// replaceLines replaces lines start..end (1-based, inclusive) of content with
// text (which may be empty: the lines are removed).
func replaceLines(content string, start, end int, text string) (string, error) {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if start < 1 || end > len(lines) || start > end {
		return "", conflict("lines %d to %d are not in the file any more", start, end)
	}
	var rep []string
	if text != "" || strings.Contains(text, "\n") {
		rep = strings.SplitAfter(text, "\n")
		if rep[len(rep)-1] == "" {
			rep = rep[:len(rep)-1]
		} else {
			rep[len(rep)-1] += "\n"
		}
	}
	if text == "" {
		rep = nil
	}
	out := append(append(append([]string{}, lines[:start-1]...), rep...), lines[end:]...)
	return strings.Join(out, ""), nil
}

// ident is the environment that makes a commit by a person and the server.
func ident(name string, now interface{ Unix() int64 }) []string {
	date := fmt.Sprintf("%d +0000", now.Unix())
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + name + "@users.noreply.launchway.invalid", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=Launchway", "GIT_COMMITTER_EMAIL=noreply@launchway.invalid", "GIT_COMMITTER_DATE=" + date,
	}
}
