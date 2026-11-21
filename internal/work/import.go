package work

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ImportReport says what an import did, or with Preview what it would do.
type ImportReport struct {
	Preview   bool     `json:"preview"`
	Created   int      `json:"created"`
	Existing  int      `json:"existing"` // already there from an earlier run: left alone
	Updated   int      `json:"updated"`
	Comments  int      `json:"comments"`
	Links     int      `json:"links"`
	Conflicts []string `json:"conflicts,omitempty"` // numbers or keys that could not be taken
	Warnings  []string `json:"warnings,omitempty"`
}

func archiveKey(repoID string, n int) string { return fmt.Sprintf("archive:%s#%d", repoID, n) }

// PromoteArchive turns the read-only archive of a repository's imported issues
// into live items of the project: the numbers stay, authors stay as text,
// dates, comments, labels and state are kept, and pull requests the archive
// recorded link to the issues they named. Running it again changes nothing.
// With preview it only counts.
func (s *Store) PromoteArchive(ctx context.Context, c Caller, sc Scope, repoID, repoPath string, preview bool) (*ImportReport, error) {
	if sc.Kind != ScopeProject {
		return nil, invalid("an archive is promoted into a project")
	}
	if !c.canWrite(sc) || (!c.isMaintainer(sc) && c.Kind != KindSystem) {
		return nil, forbidden("only a maintainer promotes an archive into %s", sc)
	}
	rep := &ImportReport{Preview: preview}
	rows, err := s.db.Query(ctx, `SELECT number, title, state, author, body, labels, milestone, assignees, is_pull, created_at, updated_at, closed_at FROM imported_issues WHERE repo_id = ? ORDER BY number`, repoID)
	if err != nil {
		return nil, err
	}
	var all []archIssue
	for rows.Next() {
		var x archIssue
		if err := rows.Scan(&x.n, &x.title, &x.state, &x.author, &x.body, &x.labels, &x.milestone, &x.assigned, &x.pull, &x.created, &x.updated, &x.closed); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, x)
	}
	rows.Close()
	if len(all) == 0 {
		return nil, notFound("this project has no imported issues to promote")
	}
	eff, err := s.Settings(ctx, sc)
	if err != nil {
		return nil, err
	}
	sys := System("import")
	done := eff.FirstOf(CatDone)
	todo := eff.FirstOf(CatTodo)
	for _, x := range all {
		key := archiveKey(repoID, x.n)
		if x.pull == 1 {
			continue // pull requests become links below
		}
		var existing string
		err := s.db.QueryRow(ctx, `SELECT id FROM work_items WHERE scope_kind = ? AND scope_id = ? AND source_key = ?`, sc.Kind, sc.ID, key).Scan(&existing)
		if err == nil {
			rep.Existing++
			if !preview {
				n, err := s.topUpComments(ctx, repoID, x.n, existing)
				if err != nil {
					return nil, err
				}
				rep.Comments += n
			}
			continue
		}
		if !isNoRows(err) {
			return nil, err
		}
		var taken int
		if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND number = ?`, sc.Kind, sc.ID, x.n).Scan(&taken); err != nil {
			return nil, err
		}
		if taken > 0 {
			rep.Conflicts = append(rep.Conflicts, fmt.Sprintf("#%d: a live item already has that number", x.n))
			continue
		}
		rep.Created++
		if preview {
			var nc int
			_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM imported_comments WHERE repo_id = ? AND issue_number = ?`, repoID, x.n).Scan(&nc)
			rep.Comments += nc
			continue
		}
		var labels, assignees []string
		_ = json.Unmarshal([]byte(x.labels), &labels)
		_ = json.Unmarshal([]byte(x.assigned), &assignees)
		typ := TypeIssue
		for _, l := range labels {
			if strings.EqualFold(l, "bug") {
				typ = TypeBug
			}
		}
		state := todo
		if x.state == "closed" {
			state = done
		}
		in := NewItem{Scope: sc, Type: typ, Title: x.title, Body: x.body, State: state, Labels: labels, Reporter: x.author, SourceKey: key, Number: x.n,
			CreatedAt: iso(x.created), ClosedAt: iso(x.closed)}
		if x.milestone != "" {
			if _, err := s.ensureMilestone(ctx, sc, x.milestone); err == nil {
				in.Milestone = x.milestone
			}
		}
		if in.Reporter == "" {
			in.Reporter = "unknown"
		}
		for _, a := range assignees {
			if a != "" {
				in.Assignees = append(in.Assignees, "person:"+a)
			}
		}
		in.Assignees = nil // assignees are kept as text below, never matched to accounts
		it, err := s.Create(ctx, sys, in)
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("#%d: %v", x.n, err))
			rep.Created--
			continue
		}
		if len(assignees) > 0 {
			for _, a := range assignees {
				_, _ = s.db.Exec(ctx, `INSERT INTO work_assignees(item_id, who, kind) SELECT ?,?,'person' WHERE NOT EXISTS (SELECT 1 FROM work_assignees WHERE item_id = ? AND who = ?)`, it.ID, a, it.ID, a)
			}
		}
		if _, err := s.db.Exec(ctx, `UPDATE work_items SET updated_at = ? WHERE id = ?`, x.updated, it.ID); err != nil {
			return nil, err
		}
		n, err := s.topUpComments(ctx, repoID, x.n, it.ID)
		if err != nil {
			return nil, err
		}
		rep.Comments += n
	}
	if err := s.promotePullLinks(ctx, sc, repoID, repoPath, preview, rep, all); err != nil {
		return nil, err
	}
	// Commits the issue texts name by hash.
	if !preview {
		rep.Links += s.linkArchiveCommits(ctx, sc, repoID, repoPath)
	}
	return rep, nil
}

var shaPattern = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

func (s *Store) linkArchiveCommits(ctx context.Context, sc Scope, repoID, repoPath string) int {
	rows, err := s.db.Query(ctx, `SELECT i.id, d.body FROM work_items i JOIN imported_issues d ON d.repo_id = ? AND i.source_key = 'archive:' || ? || '#' || d.number WHERE i.scope_kind = ? AND i.scope_id = ?`, repoID, repoID, sc.Kind, sc.ID)
	if err != nil {
		return 0
	}
	type pair struct{ id, body string }
	var ps []pair
	for rows.Next() {
		var p pair
		if rows.Scan(&p.id, &p.body) == nil {
			ps = append(ps, p)
		}
	}
	rows.Close()
	n := 0
	for _, p := range ps {
		for _, sha := range shaPattern.FindAllString(p.body, 5) {
			var had int
			_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_code_links WHERE item_id = ? AND kind = 'commit' AND repo = ? AND ref = ?`, p.id, repoPath, sha).Scan(&had)
			if had == 0 {
				if _, err := s.LinkCode(ctx, System("import"), p.id, CodeLink{Kind: CodeCommit, Repo: repoPath, Ref: sha, Source: "archive"}); err == nil {
					n++
				}
			}
		}
	}
	return n
}

func (s *Store) ensureMilestone(ctx context.Context, sc Scope, title string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM work_milestones WHERE scope_kind = ? AND scope_id = ? AND title = ?`, sc.Kind, sc.ID, title).Scan(&id)
	if err == nil {
		return id, nil
	}
	m, err := s.CreateMilestone(ctx, System("import"), sc, MilestoneIn{Title: title})
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// topUpComments copies the archive's comments of an issue that the live item
// does not have yet (keeping author text and times).
func (s *Store) topUpComments(ctx context.Context, repoID string, number int, itemID string) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT position, author, body, created_at, updated_at FROM imported_comments WHERE repo_id = ? AND issue_number = ? ORDER BY position`, repoID, number)
	if err != nil {
		return 0, err
	}
	type cm struct {
		pos          int
		author, body string
		created, upd int64
	}
	var cs []cm
	for rows.Next() {
		var x cm
		if err := rows.Scan(&x.pos, &x.author, &x.body, &x.created, &x.upd); err != nil {
			rows.Close()
			return 0, err
		}
		cs = append(cs, x)
	}
	rows.Close()
	n := 0
	for _, x := range cs {
		id := fmt.Sprintf("wcm_a%s_%d_%d", shortHash(repoID), number, x.pos)
		res, err := s.db.Exec(ctx, `INSERT INTO work_comments(id, item_id, author, author_kind, body, created_at, edited_at)
			SELECT ?,?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM work_comments WHERE id = ?)`, id, itemID, x.author, KindPerson, x.body, x.created, 0, id)
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, nil
}

func shortHash(s string) string { return hashToken(s)[:8] }

// ---- the story files ----

// StoryDoc is one object of a stories or epics JSON file.
type StoryDoc struct {
	Key       string   `json:"key"`
	Epic      string   `json:"epic"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Labels    []string `json:"labels"`
	Milestone string   `json:"milestone"`
	DependsOn []string `json:"depends_on"`
	Rank      float64  `json:"rank"`
	Phase     string   `json:"phase"`
	Doc       string   `json:"doc"`
}

var criteriaHeading = regexp.MustCompile(`(?mi)^\*\*(acceptance criteria|acceptance|done when)\*\*\s*$`)
var checkbox = regexp.MustCompile(`^\s*- \[( |x|X)\] (.+)$`)

// parseCriteria reads the checklist under an "Acceptance criteria" heading.
func parseCriteria(body string) []string {
	loc := criteriaHeading.FindStringIndex(body)
	if loc == nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(body[loc[1]:], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "**") {
			break
		}
		if m := checkbox.FindStringSubmatch(line); m != nil {
			out = append(out, strings.TrimSpace(m[2]))
		}
	}
	if len(out) > MaxCriteria {
		out = out[:MaxCriteria]
	}
	return out
}

// ImportStories loads the draft-story JSON shape: epics and stories with keys,
// bodies with acceptance criteria, labels, milestones and dependencies. It is
// idempotent by key: a key already imported is left alone (or updated, with
// update), so running the same file twice changes nothing.
func (s *Store) ImportStories(ctx context.Context, c Caller, sc Scope, docs []StoryDoc, preview, update bool) (*ImportReport, error) {
	if !c.canWrite(sc) || (!c.isMaintainer(sc) && c.Kind != KindSystem) {
		return nil, forbidden("only a maintainer imports work into %s", sc)
	}
	if len(docs) == 0 || len(docs) > 5000 {
		return nil, invalid("a file holds 1 to 5000 items")
	}
	rep := &ImportReport{Preview: preview}
	seen := map[string]bool{}
	for _, d := range docs {
		if d.Key == "" || d.Title == "" || seen[d.Key] {
			return nil, invalid("every item has a unique key and a title (problem at %q)", d.Key)
		}
		seen[d.Key] = true
	}
	// Epics first, so stories find their parents.
	ordered := make([]StoryDoc, len(docs))
	copy(ordered, docs)
	sort.SliceStable(ordered, func(i, j int) bool { return docType(ordered[i]) == TypeEpic && docType(ordered[j]) != TypeEpic })
	sys := System("import")
	ids := map[string]string{}
	find := func(key string) string {
		if id, ok := ids[key]; ok {
			return id
		}
		var id string
		if s.db.QueryRow(ctx, `SELECT id FROM work_items WHERE scope_kind = ? AND scope_id = ? AND source_key = ?`, sc.Kind, sc.ID, key).Scan(&id) == nil {
			ids[key] = id
		}
		return id
	}
	for _, d := range ordered {
		existing := find(d.Key)
		if existing != "" {
			rep.Existing++
			if update && !preview {
				if _, err := s.Update(ctx, sys, existing, Patch{Title: &d.Title, Body: &d.Body}); err != nil {
					rep.Warnings = append(rep.Warnings, d.Key+": "+err.Error())
				} else {
					rep.Updated++
				}
			}
			continue
		}
		rep.Created++
		if preview {
			continue
		}
		in := NewItem{Scope: sc, Type: docType(d), Title: truncate(d.Title, MaxTitle), Body: d.Body, Labels: limitLabels(d.Labels), SourceKey: d.Key, Criteria: parseCriteria(d.Body)}
		if d.Milestone != "" {
			if _, err := s.ensureMilestone(ctx, sc, d.Milestone); err == nil {
				in.Milestone = d.Milestone
			}
		}
		if d.Epic != "" {
			if pid := find(d.Epic); pid != "" {
				in.Parent = pid
			} else {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: its epic %s is not in the project or the file", d.Key, d.Epic))
			}
		}
		it, err := s.Create(ctx, sys, in)
		if err != nil {
			rep.Created--
			rep.Warnings = append(rep.Warnings, d.Key+": "+err.Error())
			continue
		}
		ids[d.Key] = it.ID
	}
	// Dependencies: what a story depends on blocks it.
	for _, d := range docs {
		for _, dep := range d.DependsOn {
			from, to := find(dep), find(d.Key)
			if to == "" {
				continue
			}
			if from == "" {
				if !preview {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s depends on %s, which is not in the project or the file", d.Key, dep))
				}
				continue
			}
			var had int
			_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_links WHERE from_id = ? AND to_id = ? AND kind = 'blocks'`, from, to).Scan(&had)
			if had > 0 {
				continue
			}
			rep.Links++
			if preview {
				continue
			}
			if _, err := s.AddLink(ctx, sys, from, LinkBlocks, to); err != nil {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s depends on %s: %v", d.Key, dep, err))
				rep.Links--
			}
		}
	}
	return rep, nil
}

func docType(d StoryDoc) string {
	switch {
	case d.Epic != "":
		return TypeStory
	case strings.HasPrefix(d.Key, "E") && len(d.Key) > 1 && d.Key[1] >= '0' && d.Key[1] <= '9':
		return TypeEpic
	case strings.HasPrefix(d.Key, "S") && len(d.Key) > 1 && d.Key[1] >= '0' && d.Key[1] <= '9':
		return TypeStory
	}
	return TypeIssue
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func limitLabels(in []string) []string {
	var out []string
	for _, l := range in {
		if cl, err := cleanLabel(l); err == nil {
			out = append(out, cl)
		}
		if len(out) >= MaxLabels {
			break
		}
	}
	return out
}

// archIssue is a row of the archive.
type archIssue struct {
	n                           int
	title, state, author, body  string
	labels, milestone, assigned string
	pull                        int
	created, updated, closed    int64
}

// promotePullLinks links the pull requests the archive recorded to the issues
// they named.
func (s *Store) promotePullLinks(ctx context.Context, sc Scope, repoID, repoPath string, preview bool, rep *ImportReport, all []archIssue) error {
	sys := System("import")
	for _, x := range all {
		if x.pull != 1 {
			continue
		}
		for _, r := range ParseReferences(x.title + "\n" + x.body) {
			if r.Repo != "" || r.Prefix != "" || r.Number <= 0 {
				continue
			}
			var id string
			if err := s.db.QueryRow(ctx, `SELECT id FROM work_items WHERE scope_kind = ? AND scope_id = ? AND source_key = ?`, sc.Kind, sc.ID, archiveKey(repoID, r.Number)).Scan(&id); err != nil {
				continue
			}
			var had int
			_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM work_code_links WHERE item_id = ? AND kind = 'pull' AND repo = ? AND ref = ?`, id, repoPath, fmt.Sprint(x.n)).Scan(&had)
			if had > 0 {
				continue
			}
			rep.Links++
			if preview {
				continue
			}
			st := "closed"
			if x.state == "open" {
				st = "open"
			}
			if _, err := s.LinkCode(ctx, sys, id, CodeLink{Kind: CodePull, Repo: repoPath, Ref: fmt.Sprint(x.n), Title: x.title, State: st, Closes: r.Closes, Source: "archive"}); err != nil {
				return err
			}
		}
	}
	return nil
}
