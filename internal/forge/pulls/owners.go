package pulls

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// OwnersFile is where a repository says who owns which files: a plain text
// file, read from the target branch (never from the pull request's own
// branch, so a change cannot give itself an owner).
const OwnersFile = ".launchway/owners"

// The owners file's limits.
const (
	maxOwnersBytes   = 64 * 1024
	maxOwnersRules   = 1000
	maxOwnersPerRule = 50
	groupPrefix      = "group:"
)

// OwnerRule is one line of the owners file: a path pattern and who owns what
// it matches. A pattern with no owners makes what it matches unowned again.
type OwnerRule struct {
	Line    int      `json:"line"`
	Pattern string   `json:"pattern"`
	Owners  []string `json:"owners"`
	re      *regexp.Regexp
}

// OwnerProblem is one thing wrong with an owners file, in plain words.
type OwnerProblem struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// OwnersDoc is a parsed owners file.
type OwnersDoc struct {
	Rules    []OwnerRule    `json:"rules"`
	Problems []OwnerProblem `json:"problems"`
}

// validOwnerPattern checks a path pattern: docs/, *.go, /cmd/**, src/*/gen.
func validOwnerPattern(p string) error {
	switch {
	case p == "":
		return invalid("a path pattern is empty")
	case len(p) > 300:
		return invalid("the pattern %q is longer than 300 characters", p[:40]+"...")
	case strings.HasPrefix(p, "!"):
		return invalid("%q: patterns cannot be negated; list the files that are owned, and end the list with a pattern with no owners to make files unowned again", p)
	case strings.ContainsAny(p, "[]{}\\ \t\x00\n\r"):
		return invalid("%q: patterns use only names, * (within a name), ** (any folders), ? and a trailing / for a folder", p)
	case strings.Contains(p, "..") && (p == ".." || strings.Contains(p, "../") || strings.HasSuffix(p, "/..")):
		return invalid("%q: a pattern cannot leave the repository with ..", p)
	case strings.Contains(p, "***"):
		return invalid("%q: use ** for any folders and * for part of a name", p)
	}
	return nil
}

// compilePattern turns a path pattern into a matcher: a pattern with no / in
// it (but a trailing one) matches at any depth; one with a / is anchored at
// the root of the repository; a trailing / means a folder and all below it.
func compilePattern(p string) (*regexp.Regexp, error) {
	dir := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	anchored := strings.HasPrefix(p, "/") || strings.Contains(p, "/")
	p = strings.TrimPrefix(p, "/")
	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				i++
				if i+1 < len(p) && p[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if dir {
		b.WriteString("/.*")
	} else {
		b.WriteString("(?:/.*)?")
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// MatchPath reports whether a path pattern of the owners file (or of a rule's
// paths) covers a file.
func MatchPath(pattern, file string) bool {
	re, err := compilePattern(pattern)
	return err == nil && re.MatchString(file)
}

func validOwner(tok string) error {
	name := strings.TrimPrefix(tok, "@")
	name = strings.TrimPrefix(name, groupPrefix)
	switch {
	case !strings.HasPrefix(tok, "@"):
		return fmt.Errorf("%q is not an owner: write a person as @name and a group as @group:name", tok)
	case name == "" || len(name) > 100:
		return fmt.Errorf("%q has no name after the @", tok)
	case strings.ContainsAny(name, " \t,;:/\\<>\"'`\x00"):
		return fmt.Errorf("%q is not a valid name: use letters, digits, dashes, underscores and dots", tok)
	}
	return nil
}

// ParseOwners reads an owners file. It never fails: what is wrong is listed
// in Problems with its line, and the lines that are right still count, but a
// file with problems is refused when it is checked (see Valid).
func ParseOwners(text string) OwnersDoc {
	doc := OwnersDoc{Rules: []OwnerRule{}, Problems: []OwnerProblem{}}
	if len(text) > maxOwnersBytes {
		doc.Problems = append(doc.Problems, OwnerProblem{0, fmt.Sprintf("the file is %d KiB, more than the %d KiB allowed", len(text)/1024, maxOwnersBytes/1024)})
		return doc
	}
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	for n, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		n++
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		} else if i := strings.Index(line, "\t#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		pat, who := fields[0], fields[1:]
		if err := validOwnerPattern(pat); err != nil {
			doc.Problems = append(doc.Problems, OwnerProblem{n, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")})
			continue
		}
		re, err := compilePattern(pat)
		if err != nil {
			doc.Problems = append(doc.Problems, OwnerProblem{n, fmt.Sprintf("%q is not a valid pattern", pat)})
			continue
		}
		if len(who) > maxOwnersPerRule {
			doc.Problems = append(doc.Problems, OwnerProblem{n, fmt.Sprintf("%s has %d owners, more than the %d allowed", pat, len(who), maxOwnersPerRule)})
			continue
		}
		bad := false
		seen := map[string]bool{}
		owners := []string{}
		for _, w := range who {
			if err := validOwner(w); err != nil {
				doc.Problems = append(doc.Problems, OwnerProblem{n, err.Error()})
				bad = true
				continue
			}
			if !seen[w] {
				seen[w] = true
				owners = append(owners, strings.TrimPrefix(w, "@"))
			}
		}
		if bad {
			continue
		}
		if len(doc.Rules) >= maxOwnersRules {
			doc.Problems = append(doc.Problems, OwnerProblem{n, fmt.Sprintf("more than %d rules", maxOwnersRules)})
			break
		}
		doc.Rules = append(doc.Rules, OwnerRule{Line: n, Pattern: pat, Owners: owners, re: re})
	}
	return doc
}

// Valid is nil when the file has no problems, else one error naming the first
// few with their lines.
func (d OwnersDoc) Valid() error {
	if len(d.Problems) == 0 {
		return nil
	}
	var parts []string
	for i, p := range d.Problems {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(d.Problems)-3))
			break
		}
		if p.Line > 0 {
			parts = append(parts, fmt.Sprintf("line %d: %s", p.Line, p.Message))
		} else {
			parts = append(parts, p.Message)
		}
	}
	return invalid("%s has problems (%s)", OwnersFile, strings.Join(parts, "; "))
}

// OwnersOf returns who owns a file: the owners of the last line that matches
// it (nil when no line does, or the last matching line names no one).
func (d OwnersDoc) OwnersOf(file string) []string {
	var out []string
	for _, r := range d.Rules {
		if r.re.MatchString(file) {
			out = r.Owners
		}
	}
	return out
}

// FileOwners says who owns one changed file.
type FileOwners struct {
	Path   string   `json:"path"`
	Owners []string `json:"owners"`
}

// OwnersView is the owners of a pull request's changed files.
type OwnersView struct {
	// Present says the target branch has an owners file.
	Present  bool           `json:"present"`
	Problems []OwnerProblem `json:"problems"`
	Files    []FileOwners   `json:"files"`
	// All are the distinct owners of the changed files, sorted.
	All []string `json:"owners"`
}

// Owners reads the owners file at the target branch's tip and says who owns
// each file the pull request changes. A file with problems is reported, and
// owns nothing: a broken file never blocks by itself being unreadable, but the
// owner-approval rule refuses to pass while it is broken (see Readiness).
func (s *Service) Owners(ctx context.Context, repoID string, p PullRequest, files []string) (OwnersView, OwnersDoc, error) {
	v := OwnersView{Problems: []OwnerProblem{}, Files: []FileOwners{}, All: []string{}}
	var text string
	var found bool
	err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		text, found, err = g.readFile(ctx, p.BaseSHA, OwnersFile)
		return err
	})
	if err != nil {
		return v, OwnersDoc{}, err
	}
	if !found {
		return v, OwnersDoc{}, nil
	}
	doc := ParseOwners(text)
	v.Present, v.Problems = true, doc.Problems
	all := map[string]bool{}
	for _, f := range files {
		o := doc.OwnersOf(f)
		if o == nil {
			o = []string{}
		}
		v.Files = append(v.Files, FileOwners{f, o})
		for _, w := range o {
			all[w] = true
		}
	}
	for w := range all {
		v.All = append(v.All, w)
	}
	sort.Strings(v.All)
	return v, doc, nil
}

// ChangedPaths lists the files a pull request changes (the new path of a
// rename and the old one, since both are touched).
func (s *Service) ChangedPaths(ctx context.Context, repoID string, p PullRequest) ([]string, error) {
	d, err := s.Files(ctx, repoID, p, DefaultLimits)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, f := range d.Files {
		for _, n := range []string{f.Path, f.OldPath} {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
