package refrules

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Writer is a rule that admits a pipeline identity to a protected branch: a
// deploy step of a project matching Projects may push to the branches
// matching Branch, and what it pushes may change only files matching Files
// (where ** matches any number of directories). Nothing else about the
// branch's protection is waived: a person still cannot push there, and the
// identity cannot push anywhere no rule names.
type Writer struct {
	Branch   string   `json:"branch"`
	Projects []string `json:"projects"`
	Files    []string `json:"files"`
}

const (
	maxWriters      = 50
	maxWriterFields = 100
)

// ValidateWriters checks rules before they are stored.
func ValidateWriters(ws []Writer) error {
	if len(ws) > maxWriters {
		return fmt.Errorf("at most %d pipeline writer rules", maxWriters)
	}
	for i, w := range ws {
		at := fmt.Sprintf("pipeline writer %d", i+1)
		if err := Validate(w.Branch); err != nil {
			return fmt.Errorf("%s: %w", at, err)
		}
		if len(w.Projects) == 0 || len(w.Files) == 0 {
			return fmt.Errorf("%s: name the projects and the files it admits; a rule with no files would admit nothing", at)
		}
		if len(w.Projects) > maxWriterFields || len(w.Files) > maxWriterFields {
			return fmt.Errorf("%s: at most %d projects and %d files", at, maxWriterFields, maxWriterFields)
		}
		for _, p := range w.Projects {
			if _, err := path.Match(p, ""); err != nil || p == "" {
				return fmt.Errorf("%s: %q is not a project pattern", at, p)
			}
		}
		for _, f := range w.Files {
			if f == "" || strings.HasPrefix(f, "/") || strings.Contains(f, "..") || strings.ContainsAny(f, "\\\x00\r\n") {
				return fmt.Errorf("%s: %q is not a file path or pattern relative to the repository's root", at, f)
			}
			if f == "**" {
				return fmt.Errorf("%s: ** would admit every file; name the files the pipeline writes", at)
			}
			if _, err := path.Match(strings.ReplaceAll(f, "**", "*"), ""); err != nil {
				return fmt.Errorf("%s: %q is not a valid pattern", at, f)
			}
		}
	}
	return nil
}

// Writers returns a repository's pipeline writer rules.
func (s *Store) Writers(ctx context.Context, repoID string) ([]Writer, error) {
	rows, err := s.db.Query(ctx, `SELECT branch, projects, files FROM branch_writers WHERE repo_id = ? ORDER BY created_at, id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Writer{}
	for rows.Next() {
		var w Writer
		var projects, files string
		if err := rows.Scan(&w.Branch, &projects, &files); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(projects), &w.Projects) != nil || json.Unmarshal([]byte(files), &w.Files) != nil {
			continue // a row nothing can read admits nothing
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetWriters replaces a repository's pipeline writer rules.
func (s *Store) SetWriters(ctx context.Context, repoID string, ws []Writer, by string) error {
	if err := ValidateWriters(ws); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM branch_writers WHERE repo_id = ?`, repoID); err != nil {
		return err
	}
	for i, w := range ws {
		projects, _ := json.Marshal(w.Projects)
		files, _ := json.Marshal(w.Files)
		if _, err := s.db.Exec(ctx, `INSERT INTO branch_writers(id, repo_id, branch, projects, files, created_at, created_by) VALUES(?,?,?,?,?,?,?)`,
			fmt.Sprintf("wr_%s_%d", repoID, i), repoID, w.Branch, string(projects), string(files), s.now().UnixMilli()+int64(i), by); err != nil {
			return err
		}
	}
	return nil
}

// Admits judges what a pipeline identity of a project pushes to a branch:
// nil if some rule admits the project to the branch and covers every file,
// otherwise the sentence the pusher is told.
func Admits(ws []Writer, branch, project string, files []string) error {
	var rules []Writer
	for _, w := range ws {
		if m, _ := path.Match(w.Branch, branch); m && matchAny(w.Projects, project) {
			rules = append(rules, w)
		}
	}
	if len(rules) == 0 {
		return fmt.Errorf("no rule of this repository admits a pipeline of project %s to %s: an administrator adds one under the branch's pipeline writers, naming exactly the files it may write", project, branch)
	}
	for _, f := range files {
		ok := false
		for _, w := range rules {
			for _, g := range w.Files {
				if globMatch(g, f) {
					ok = true
				}
			}
		}
		if !ok {
			return fmt.Errorf("the pipeline of project %s may not change %s on %s: its rules admit only %s", project, f, branch, admittedFiles(rules))
		}
	}
	return nil
}

func admittedFiles(rules []Writer) string {
	var all []string
	for _, w := range rules {
		all = append(all, w.Files...)
	}
	return strings.Join(all, ", ")
}

func matchAny(patterns []string, v string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, v); ok {
			return true
		}
	}
	return false
}

// globMatch matches a path against a pattern where a part ** matches any
// number of directories (none included).
func globMatch(pattern, name string) bool {
	return matchParts(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchParts(ps, ns []string) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			for i := 0; i <= len(ns); i++ {
				if matchParts(ps[1:], ns[i:]) {
					return true
				}
			}
			return false
		}
		if len(ns) == 0 {
			return false
		}
		if ok, _ := path.Match(ps[0], ns[0]); !ok {
			return false
		}
		ps, ns = ps[1:], ns[1:]
	}
	return len(ns) == 0
}
