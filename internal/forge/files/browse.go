package files

// What reading code needs beyond one file at a time: the last commit of every
// entry of a directory, branches and tags, one commit with its parents, blame,
// search, a raw file, and making and removing a branch. Everything reads the
// repository through the hosting storage; nothing here is a second store.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// LastCommit is the newest commit that changed an entry.
type LastCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	At      string `json:"at"`
}

// The bounds of the last-commit column. A directory is read by walking the
// history of that directory only, newest first, in rounds of growing size
// (each round is one git process that lists the files each commit changed
// under the directory) until every entry has been seen or the bounds are
// reached. Entries last changed further back show no commit and the page says so.
const (
	lastMaxCommits = 4000
	lastBudget     = 6 * time.Second
	lastCacheSize  = 512
)

var lastRounds = []int{50, 200, 750, 3000}

type lastKey struct{ repo, commit, dir string }
type lastVal struct {
	m       map[string]LastCommit
	bounded bool
}

var lastCache = struct {
	sync.Mutex
	m     map[lastKey]lastVal
	order []lastKey
}{m: map[lastKey]lastVal{}}

func cacheGet(k lastKey) (lastVal, bool) {
	lastCache.Lock()
	defer lastCache.Unlock()
	v, ok := lastCache.m[k]
	return v, ok
}

func cachePut(k lastKey, v lastVal) {
	lastCache.Lock()
	defer lastCache.Unlock()
	if _, ok := lastCache.m[k]; !ok {
		lastCache.order = append(lastCache.order, k)
		if len(lastCache.order) > lastCacheSize {
			delete(lastCache.m, lastCache.order[0])
			lastCache.order = lastCache.order[1:]
		}
	}
	lastCache.m[k] = v
}

// ReadWithLast is Read, and for a directory the last commit of each entry.
// The result for a commit and a directory never changes, so it is kept.
func (s *Service) ReadWithLast(ctx context.Context, repoID, ref, p string) (Item, error) {
	it, err := s.Read(ctx, repoID, ref, p)
	if err != nil || it.Type != "dir" || len(it.Entries) == 0 {
		return it, err
	}
	key := lastKey{repoID, it.Commit, it.Path}
	v, ok := cacheGet(key)
	if !ok {
		err = s.Git.WithCache(ctx, repoID, func(dir string) error {
			g := runner{dir: dir, limits: s.Git.Limits()}
			names := make([]string, len(it.Entries))
			for i, e := range it.Entries {
				names[i] = e.Name
			}
			m, bounded := g.lastCommits(ctx, it.Commit, it.Path, names)
			v = lastVal{m, bounded}
			return nil
		})
		if err != nil {
			return it, err
		}
		cachePut(key, v)
	}
	for i := range it.Entries {
		if c, ok := v.m[it.Entries[i].Name]; ok {
			c := c
			it.Entries[i].Last = &c
		}
	}
	it.LastBounded = v.bounded
	return it, nil
}

// lastCommits finds, for each name in dir, the newest commit (merges aside)
// that changed it or something under it. bounded is true when some names
// were still unseen at the bounds.
func (g runner) lastCommits(ctx context.Context, commit, dir string, names []string) (map[string]LastCommit, bool) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	out := map[string]LastCommit{}
	ctx, cancel := context.WithTimeout(ctx, lastBudget)
	defer cancel()
	skip, seen := 0, 0
	for _, n := range lastRounds {
		if seen >= lastMaxCommits || len(out) == len(want) {
			break
		}
		args := []string{"log", "-z", "--no-merges", "--no-renames", "--name-only", "-n", strconv.Itoa(n), "--skip=" + strconv.Itoa(skip),
			"--format=%x01%H%x1f%an%x1f%aI%x1f%s", commit, "--"}
		if dir != "" {
			args = append(args, dir)
		} else {
			args = append(args, ".")
		}
		raw, err := g.run(ctx, nil, args...)
		if err != nil {
			break
		}
		count := 0
		for _, chunk := range strings.Split(raw, "\x01") {
			if chunk == "" {
				continue
			}
			count++
			head, rest, _ := strings.Cut(chunk, "\x00")
			f := strings.SplitN(head, "\x1f", 4)
			if len(f) < 4 {
				continue
			}
			lc := LastCommit{SHA: f[0], Author: f[1], At: f[2], Subject: f[3]}
			for _, name := range strings.Split(rest, "\x00") {
				name = strings.TrimLeft(name, "\n")
				if name == "" {
					continue
				}
				if dir != "" {
					name = strings.TrimPrefix(name, dir+"/")
				}
				first, _, _ := strings.Cut(name, "/")
				if want[first] {
					if _, done := out[first]; !done {
						out[first] = lc
					}
				}
			}
		}
		seen += count
		skip += n
		if count < n {
			return out, false // the history ended
		}
	}
	return out, len(out) < len(want)
}

// RefInfo is a branch or a tag with what a list of them shows.
type RefInfo struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // branch or tag
	SHA         string `json:"sha"`  // what the ref points at (the tag object for an annotated tag)
	Commit      string `json:"commit"`
	Subject     string `json:"subject"`
	Author      string `json:"author"`
	CommittedAt string `json:"committed_at"`
	Annotated   bool   `json:"annotated,omitempty"`
	Message     string `json:"message,omitempty"` // an annotated tag's message
	Tagger      string `json:"tagger,omitempty"`
	// Ahead and Behind are the commits the branch has that the base lacks and
	// the reverse; -1 when they could not be counted.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
}

// Refs lists branches or tags, newest first, those whose name contains q.
// base is the branch ahead and behind are counted against (branches only).
func (s *Service) Refs(ctx context.Context, repoID, kind, q, base string, limit int) (items []RefInfo, total int, err error) {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	prefix := "refs/heads/"
	if kind == "tag" {
		prefix = "refs/tags/"
	} else if kind != "branch" {
		return nil, 0, invalid("kind is branch or tag")
	}
	items = []RefInfo{}
	err = s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		fmtS := strings.Join([]string{"%(refname)", "%(objectname)", "%(objecttype)", "%(*objectname)", "%(authorname)", "%(*authorname)", "%(committerdate:iso-strict)", "%(*committerdate:iso-strict)", "%(subject)", "%(taggername)"}, "%1f") + "%1e"
		args := []string{"for-each-ref", "--sort=-creatordate", "--format=" + fmtS}
		if kind == "branch" && base != "" && checkRef(base) == nil {
			if sha, err := g.resolve(ctx, base); err == nil {
				args[2] = "--format=" + fmtS[:len(fmtS)-3] + "%1f%(ahead-behind:" + sha + ")%1e"
			}
		}
		raw, err := g.run(ctx, nil, append(args, prefix)...)
		if err != nil {
			return err
		}
		lq := strings.ToLower(q)
		for _, rec := range strings.Split(raw, "\x1e") {
			f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
			if len(f) < 10 || !strings.HasPrefix(f[0], prefix) {
				continue
			}
			name := strings.TrimPrefix(f[0], prefix)
			if lq != "" && !strings.Contains(strings.ToLower(name), lq) {
				continue
			}
			total++
			if len(items) >= limit {
				continue
			}
			ri := RefInfo{Name: name, Kind: kind, SHA: f[1], Commit: f[1], Author: f[4], CommittedAt: f[6], Subject: f[8], Ahead: -1, Behind: -1}
			if f[2] == "tag" {
				ri.Annotated, ri.Commit, ri.Author, ri.CommittedAt, ri.Tagger = true, f[3], f[5], f[7], f[9]
				ri.Message = f[8]
				if body, err := g.run(ctx, nil, "cat-file", "-p", "--end-of-options", f[1]); err == nil {
					_, msg, _ := strings.Cut(body, "\n\n")
					ri.Message = strings.TrimSpace(msg)
				}
				if cs, err := g.run(ctx, nil, "log", "-1", "--format=%s", "--end-of-options", f[3]); err == nil {
					ri.Subject = strings.TrimSpace(cs)
				}
			}
			if len(f) >= 11 {
				if a, b, ok := strings.Cut(strings.TrimSpace(f[10]), " "); ok {
					ri.Ahead, _ = strconv.Atoi(a)
					ri.Behind, _ = strconv.Atoi(b)
				}
			}
			items = append(items, ri)
		}
		return nil
	})
	return items, total, err
}

// CommitDetail is one commit with its whole message and its parents.
func (s *Service) CommitDetail(ctx context.Context, repoID, rev string) (Commit, error) {
	var out Commit
	err := s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		sha, err := g.resolve(ctx, rev)
		if err != nil {
			return err
		}
		raw, err := g.run(ctx, nil, "log", "-1", "--format=%H%x1f%an%x1f%aI%x1f%cn%x1f%cI%x1f%P%x1f%B", sha)
		if err != nil {
			return err
		}
		f := strings.SplitN(raw, "\x1f", 7)
		if len(f) < 7 {
			return missing("no commit %q", rev)
		}
		msg := strings.TrimRight(f[6], "\n")
		subject, _, _ := strings.Cut(msg, "\n")
		out = Commit{SHA: f[0], Author: f[1], AuthorAt: f[2], Committer: f[3], CommitAt: f[4], Parents: len(strings.Fields(f[5])), ParentSHAs: strings.Fields(f[5]), Subject: subject, Message: msg}
		return nil
	})
	return out, err
}

// BlameLine is one line of a file and the commit that last changed it.
type BlameLine struct {
	N      int    `json:"n"`
	SHA    string `json:"sha"`
	Text   string `json:"text"`
	Origin int    `json:"origin"` // the line number in that commit
}

// BlameCommit is what the lines of a blame point at.
type BlameCommit struct {
	SHA      string `json:"sha"`
	Author   string `json:"author"`
	At       string `json:"at"`
	Subject  string `json:"subject"`
	Previous string `json:"previous,omitempty"`      // the commit before this change, for "blame prior to this change"
	PrevPath string `json:"previous_path,omitempty"` // the file's path there (it may have been renamed)
	Path     string `json:"path"`
}

// Blame is the answer for a range of lines.
type Blame struct {
	Path    string                 `json:"path"`
	Commit  string                 `json:"commit"`
	Total   int                    `json:"total"` // lines in the file
	From    int                    `json:"from"`
	To      int                    `json:"to"`
	Next    int                    `json:"next,omitempty"` // the first line of the next range, 0 at the end
	Lines   []BlameLine            `json:"lines"`
	Commits map[string]BlameCommit `json:"commits"`
}

// MaxBlameLines is how many lines one blame answer holds; a longer file is
// read range by range.
const MaxBlameLines = 1000

// BlameOf blames lines from..to (1-based, inclusive; 0 means from the start)
// of a file at a ref.
func (s *Service) BlameOf(ctx context.Context, repoID, ref, p string, from, to int) (Blame, error) {
	p, err := CleanPath(p)
	if err != nil || p == "" {
		return Blame{}, invalid("blame needs a path to a file")
	}
	var out Blame
	err = s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		commit, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		spec := commit + ":" + p
		kind, err := g.run(ctx, nil, "cat-file", "-t", "--end-of-options", spec)
		if err != nil || strings.TrimSpace(kind) != "blob" {
			return missing("no file %q at %s", p, ref)
		}
		szs, _ := g.run(ctx, nil, "cat-file", "-s", "--end-of-options", spec)
		if n, _ := strconv.ParseInt(strings.TrimSpace(szs), 10, 64); n > MaxFileBytes {
			return invalid("%s is larger than %d bytes: blame is not computed for it", p, MaxFileBytes)
		}
		body, err := g.run(ctx, nil, "cat-file", "blob", "--end-of-options", spec)
		if err != nil {
			return err
		}
		if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
			return invalid("%s is a binary file: it has no lines to blame", p)
		}
		total := strings.Count(body, "\n")
		if body != "" && !strings.HasSuffix(body, "\n") {
			total++
		}
		if from <= 0 {
			from = 1
		}
		if to <= 0 || to > total || to-from+1 > MaxBlameLines {
			to = min(total, from+MaxBlameLines-1)
		}
		out = Blame{Path: p, Commit: commit, Total: total, From: from, To: to, Lines: []BlameLine{}, Commits: map[string]BlameCommit{}}
		if total == 0 || from > total {
			return nil
		}
		raw, err := g.run(ctx, nil, "blame", "--porcelain", "-L", fmt.Sprintf("%d,%d", from, to), commit, "--", p)
		if err != nil {
			return err
		}
		parseBlame(raw, &out)
		if to < total {
			out.Next = to + 1
		}
		return nil
	})
	return out, err
}

var blameHead = regexp.MustCompile(`^([0-9a-f]{40,64}) (\d+) (\d+)(?: (\d+))?$`)

func parseBlame(raw string, out *Blame) {
	var cur string
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "\t") {
			continue
		}
		if m := blameHead.FindStringSubmatch(line); m != nil {
			cur = m[1]
			orig, _ := strconv.Atoi(m[2])
			final, _ := strconv.Atoi(m[3])
			out.Lines = append(out.Lines, BlameLine{N: final, SHA: cur, Origin: orig})
			if _, ok := out.Commits[cur]; !ok {
				out.Commits[cur] = BlameCommit{SHA: cur}
			}
			continue
		}
		c := out.Commits[cur]
		switch {
		case strings.HasPrefix(line, "author "):
			c.Author = strings.TrimPrefix(line, "author ")
		case strings.HasPrefix(line, "author-time "):
			if t, err := strconv.ParseInt(strings.TrimPrefix(line, "author-time "), 10, 64); err == nil {
				c.At = time.Unix(t, 0).UTC().Format(time.RFC3339)
			}
		case strings.HasPrefix(line, "summary "):
			c.Subject = strings.TrimPrefix(line, "summary ")
		case strings.HasPrefix(line, "previous "):
			c.Previous, c.PrevPath, _ = strings.Cut(strings.TrimPrefix(line, "previous "), " ")
		case strings.HasPrefix(line, "filename "):
			c.Path = strings.TrimPrefix(line, "filename ")
		default:
			continue
		}
		out.Commits[cur] = c
	}
	// The text of each line, in order.
	i := 0
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "\t") && i < len(out.Lines) {
			out.Lines[i].Text = line[1:]
			i++
		}
	}
}

// SearchQuery is what to look for.
type SearchQuery struct {
	Pattern string
	Regex   bool
	Case    bool   // match case
	In      string // "content" (default), "path" or "both"
	Dir     string // only under this directory
	Exts    []string
	Limit   int // matches, at most MaxSearchMatches
}

// Match is one matching line.
type Match struct {
	Line int    `json:"line"`
	Text string `json:"text"`
	// Ranges are [start,end) offsets (in runes) of the match inside Text.
	Ranges [][2]int `json:"ranges,omitempty"`
}

// FileMatches are the matches in one file.
type FileMatches struct {
	Path      string  `json:"path"`
	PathMatch bool    `json:"path_match,omitempty"`
	Matches   []Match `json:"matches"`
}

// SearchResult is the answer, with an honest account of what was not searched.
type SearchResult struct {
	Commit  string        `json:"commit"`
	Files   []FileMatches `json:"files"`
	Matches int           `json:"matches"`
	Partial bool          `json:"partial"`
	Reason  string        `json:"reason,omitempty"`
	Millis  int64         `json:"millis"`
}

// The bounds of a search.
const (
	MaxSearchMatches = 500
	maxSearchOutput  = 4 << 20
	searchBudget     = 10 * time.Second
	maxLineBytes     = 400
	maxPatternBytes  = 300
	maxFilesListed   = 200000
)

type capWriter struct {
	buf    bytes.Buffer
	max    int
	over   bool
	cancel context.CancelFunc
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		w.over = true
		w.buf.Write(p[:max(0, w.max-w.buf.Len())])
		w.cancel()
		return len(p), nil
	}
	return w.buf.Write(p)
}

// searchRun is one search in progress: what is found so far, and the ways it
// is cut short.
type searchRun struct {
	g      runner
	gd     string
	commit string
	q      SearchQuery
	dir    string
	exts   []string
	limit  int
	ctx    context.Context
	cancel context.CancelFunc
	res    *SearchResult
	by     map[string]*FileMatches
	order  []string
}

func (r *searchRun) add(p string) *FileMatches {
	if f, ok := r.by[p]; ok {
		return f
	}
	f := &FileMatches{Path: p, Matches: []Match{}}
	r.by[p] = f
	r.order = append(r.order, p)
	return f
}

func (r *searchRun) matcher() *regexp.Regexp {
	pat := r.q.Pattern
	if !r.q.Regex {
		pat = regexp.QuoteMeta(pat)
	}
	if !r.q.Case {
		pat = "(?i)" + pat
	}
	re, _ := regexp.Compile(pat)
	return re
}

// byPath lists the files of the commit and keeps those whose path matches.
func (r *searchRun) byPath() error {
	re := r.matcher()
	cw := &capWriter{max: 32 << 20, cancel: r.cancel}
	args := []string{"ls-tree", "-r", "--name-only", "-z", "--end-of-options", r.commit}
	if r.dir != "" {
		args = append(args, "--", r.dir)
	}
	if _, err := gitexec.Run(r.ctx, gitexec.Spec{GitDir: r.gd, Args: args, Timeout: searchBudget, Limits: r.g.limits, Stdout: cw}); err != nil && !cw.over && r.ctx.Err() == nil {
		return err
	}
	n := 0
	for _, p := range strings.Split(cw.buf.String(), "\x00") {
		if p == "" {
			continue
		}
		if n++; n > maxFilesListed {
			r.res.Partial, r.res.Reason = true, "the file list is longer than searched"
			break
		}
		if hasExt(p, r.exts) && re.MatchString(p) && len(r.order) < r.limit {
			r.add(p).PathMatch = true
			r.res.Matches++
		}
	}
	if cw.over {
		r.res.Partial, r.res.Reason = true, "the file list is longer than searched"
	}
	return nil
}

// pathspecs are the folder and extension limits as git pathspecs.
func (r *searchRun) pathspecs() []string {
	switch {
	case len(r.exts) > 0:
		var out []string
		for _, e := range r.exts {
			g := "**/*." + e
			if r.dir != "" {
				g = r.dir + "/" + g
			}
			out = append(out, ":(glob)"+g)
		}
		return out
	case r.dir != "":
		return []string{":(literal)" + r.dir}
	}
	return []string{"."}
}

// byContent runs git grep and reads its records: commit:path NUL line NUL text NL.
func (r *searchRun) byContent() error {
	args := []string{"grep", "-I", "-n", "-z", "--no-color", "--max-count=20"}
	if !r.q.Case {
		args = append(args, "-i")
	}
	if r.q.Regex {
		args = append(args, "-E")
	} else {
		args = append(args, "-F")
	}
	args = append(append(args, "-e", r.q.Pattern, r.commit, "--"), r.pathspecs()...)
	cw := &capWriter{max: maxSearchOutput, cancel: r.cancel}
	_, err := gitexec.Run(r.ctx, gitexec.Spec{GitDir: r.gd, Args: args, Timeout: searchBudget, Limits: r.g.limits, Stdout: cw})
	var ee *gitexec.ExitError
	if err != nil && (!errors.As(err, &ee) || ee.Code != 1) && !cw.over && r.ctx.Err() == nil {
		return err
	}
	if cw.over {
		r.res.Partial, r.res.Reason = true, "there were more matches than one answer holds"
	} else if r.ctx.Err() != nil {
		r.res.Partial, r.res.Reason = true, fmt.Sprintf("the search stopped after %d seconds; narrow it by folder or extension", int(searchBudget.Seconds()))
	}
	r.records(cw.buf.Bytes())
	return nil
}

func (r *searchRun) records(rd []byte) {
	mre := r.matcher()
	prefix := r.commit + ":"
	for len(rd) > 0 {
		i := bytes.IndexByte(rd, 0)
		if i < 0 {
			return
		}
		file := strings.TrimPrefix(string(rd[:i]), prefix)
		rd = rd[i+1:]
		j := bytes.IndexByte(rd, 0)
		if j < 0 {
			return
		}
		ln, _ := strconv.Atoi(string(rd[:j]))
		rd = rd[j+1:]
		var text string
		if k := bytes.IndexByte(rd, '\n'); k < 0 {
			text, rd = string(rd), nil
		} else {
			text, rd = string(rd[:k]), rd[k+1:]
		}
		if r.res.Matches >= r.limit {
			r.res.Partial, r.res.Reason = true, fmt.Sprintf("showing the first %d matches", r.limit)
			return
		}
		m := Match{Line: ln, Text: strings.ToValidUTF8(text, "\uFFFD")}
		if len(m.Text) > maxLineBytes {
			m.Text = strings.ToValidUTF8(m.Text[:maxLineBytes], "") + "…"
		}
		if mre != nil {
			for _, loc := range mre.FindAllStringIndex(m.Text, 10) {
				if loc[1] > loc[0] {
					m.Ranges = append(m.Ranges, [2]int{utf8.RuneCountInString(m.Text[:loc[0]]), utf8.RuneCountInString(m.Text[:loc[1]])})
				}
			}
		}
		f := r.add(file)
		f.Matches = append(f.Matches, m)
		r.res.Matches++
	}
}

// cleanQuery checks a query and returns its folder, extensions and limit.
func cleanQuery(q SearchQuery) (dir string, exts []string, limit int, err error) {
	if strings.TrimSpace(q.Pattern) == "" || len(q.Pattern) > maxPatternBytes || strings.ContainsAny(q.Pattern, "\x00\n") {
		return "", nil, 0, invalid("search for 1 to %d characters on one line", maxPatternBytes)
	}
	if q.Regex {
		if _, err := regexp.Compile(q.Pattern); err != nil {
			return "", nil, 0, invalid("that is not a regular expression: %v", err)
		}
	}
	if dir, err = CleanPath(q.Dir); err != nil {
		return "", nil, 0, err
	}
	for _, e := range q.Exts {
		e = strings.TrimPrefix(strings.TrimSpace(e), ".")
		if e == "" {
			continue
		}
		if len(e) > 16 || strings.ContainsAny(e, "/*?[]\\:\x00 ") {
			return "", nil, 0, invalid("%q is not a file extension", e)
		}
		exts = append(exts, e)
	}
	limit = q.Limit
	if limit <= 0 || limit > MaxSearchMatches {
		limit = MaxSearchMatches
	}
	return dir, exts, limit, nil
}

// Search looks for text in the files (and paths) of a repository at a ref with
// git grep over the materialized repository: no index, no other service. It
// stops at its bounds and says so.
func (s *Service) Search(ctx context.Context, repoID, ref string, q SearchQuery) (SearchResult, error) {
	started := time.Now()
	dir, exts, limit, err := cleanQuery(q)
	if err != nil {
		return SearchResult{}, err
	}
	res := SearchResult{Files: []FileMatches{}}
	err = s.Git.WithCache(ctx, repoID, func(gd string) error {
		g := runner{dir: gd, limits: s.Git.Limits()}
		commit, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		res.Commit = commit
		gctx, cancel := context.WithTimeout(ctx, searchBudget)
		defer cancel()
		r := &searchRun{g: g, gd: gd, commit: commit, q: q, dir: dir, exts: exts, limit: limit, ctx: gctx, cancel: cancel, res: &res, by: map[string]*FileMatches{}}
		in := q.In
		if in == "path" || in == "both" {
			if err := r.byPath(); err != nil {
				return err
			}
		}
		if in == "" || in == "content" || in == "both" {
			if err := r.byContent(); err != nil {
				return err
			}
		}
		for _, p := range r.order {
			res.Files = append(res.Files, *r.by[p])
		}
		return nil
	})
	res.Millis = time.Since(started).Milliseconds()
	return res, err
}

func hasExt(p string, exts []string) bool {
	if len(exts) == 0 {
		return true
	}
	for _, e := range exts {
		if strings.HasSuffix(strings.ToLower(p), "."+strings.ToLower(e)) {
			return true
		}
	}
	return false
}

// MaxRawBytes is the largest file served for download.
const MaxRawBytes = 200 << 20

// Raw streams a file's bytes to w and says its size and mode. It refuses what
// is not a file, and a file over MaxRawBytes.
func (s *Service) Raw(ctx context.Context, repoID, ref, p string, w func(size int64, mode string) io.Writer) error {
	p, err := CleanPath(p)
	if err != nil || p == "" {
		return invalid("name a file")
	}
	return s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		commit, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		ls, err := g.run(ctx, nil, "ls-tree", "-l", "-z", "--end-of-options", commit, "--", p)
		f := strings.Fields(strings.SplitN(ls, "\t", 2)[0])
		if err != nil || len(f) < 4 || f[1] != "blob" {
			return missing("no file %q at %s", p, ref)
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		if size > MaxRawBytes {
			return invalid("%s is larger than %d MiB", p, MaxRawBytes>>20)
		}
		out := w(size, f[0])
		_, err = gitexec.Run(ctx, gitexec.Spec{GitDir: dir, Args: []string{"cat-file", "blob", "--end-of-options", f[2]}, Timeout: 10 * time.Minute, Limits: g.limits, Stdout: out})
		return err
	})
}

// CreateBranch makes a branch at a commit, tag or branch.
func (s *Service) CreateBranch(ctx context.Context, repoID, name, from string, check func(ref, old, next string) error) (string, error) {
	if err := checkRef(name); err != nil || strings.HasPrefix(name, "refs/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.Contains(name, "//") {
		return "", invalid("%q is not a branch name", name)
	}
	return s.createRef(ctx, repoID, "refs/heads/", name, from, "the branch", check)
}

// createRef makes a ref under prefix at a commit, branch or tag, never moving one that exists.
func (s *Service) createRef(ctx context.Context, repoID, prefix, name, from, what string, check func(ref, old, next string) error) (string, error) {
	var created string
	_, err := s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		ref := prefix + name
		if _, ok := refs[ref]; ok {
			return nil, fmt.Errorf("%w: %s %s exists already", ErrConflict, what, name)
		}
		for r := range refs {
			if strings.HasPrefix(r, ref+"/") || strings.HasPrefix(ref, r+"/") {
				return nil, fmt.Errorf("%w: %s clashes with %s %s", ErrConflict, name, what, strings.TrimPrefix(r, prefix))
			}
		}
		g := runner{dir: scratch, limits: s.Git.Limits(), scratch: true}
		at := from
		for _, pre := range []string{"refs/heads/", "refs/tags/"} {
			if v, ok := refs[pre+from]; ok {
				at = v
				break
			}
		}
		sha, err := g.resolve(ctx, at)
		if err != nil {
			return nil, err
		}
		if check != nil {
			if err := check(ref, "", sha); err != nil {
				return nil, err
			}
		}
		created = sha
		return []storage.RefUpdate{{Name: ref, Old: strings.Repeat("0", len(sha)), New: sha}}, nil
	})
	return created, err
}

// DeleteBranch removes a branch and says which commit it pointed at, so the
// caller can tell how to bring it back.
func (s *Service) DeleteBranch(ctx context.Context, repoID, name string, check func(ref, old, next string) error) (string, error) {
	if err := checkRef(name); err != nil || strings.HasPrefix(name, "refs/") {
		return "", invalid("%q is not a branch name", name)
	}
	var was string
	_, err := s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		ref := "refs/heads/" + name
		tip, ok := refs[ref]
		if !ok {
			return nil, missing("no branch %s", name)
		}
		if check != nil {
			if err := check(ref, tip, strings.Repeat("0", len(tip))); err != nil {
				return nil, err
			}
		}
		was = tip
		return []storage.RefUpdate{{Name: ref, Old: tip, New: strings.Repeat("0", len(tip))}}, nil
	})
	return was, err
}

func validRefName(name, kind string) error {
	if err := checkRef(name); err != nil || strings.HasPrefix(name, "refs/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.Contains(name, "//") {
		return invalid("%q is not a %s name", name, kind)
	}
	return nil
}

// CreateTag makes a lightweight tag at a commit, branch or tag. A tag that exists is never moved.
func (s *Service) CreateTag(ctx context.Context, repoID, name, from string, check func(ref, old, next string) error) (string, error) {
	if err := validRefName(name, "tag"); err != nil {
		return "", err
	}
	return s.createRef(ctx, repoID, "refs/tags/", name, from, "the tag", check)
}

// DeleteTag removes a tag and says which object it pointed at.
func (s *Service) DeleteTag(ctx context.Context, repoID, name string, check func(ref, old, next string) error) (string, error) {
	if err := validRefName(name, "tag"); err != nil {
		return "", err
	}
	var was string
	_, err := s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		ref := "refs/tags/" + name
		tip, ok := refs[ref]
		if !ok {
			return nil, missing("no tag %s", name)
		}
		if check != nil {
			if err := check(ref, tip, strings.Repeat("0", len(tip))); err != nil {
				return nil, err
			}
		}
		was = tip
		return []storage.RefUpdate{{Name: ref, Old: tip, New: strings.Repeat("0", len(tip))}}, nil
	})
	return was, err
}

// RenameBranch moves a branch to a new name in one step: the new name is made
// at the old tip and the old name removed. Both changes are checked as pushes.
func (s *Service) RenameBranch(ctx context.Context, repoID, name, to string, check func(ref, old, next string) error) (string, error) {
	if err := validRefName(to, "branch"); err != nil {
		return "", err
	}
	if err := validRefName(name, "branch"); err != nil {
		return "", err
	}
	var tip string
	_, err := s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		from, dest := "refs/heads/"+name, "refs/heads/"+to
		sha, ok := refs[from]
		if !ok {
			return nil, missing("no branch %s", name)
		}
		if _, ok := refs[dest]; ok {
			return nil, fmt.Errorf("%w: the branch %s exists already", ErrConflict, to)
		}
		for r := range refs {
			if r != from && (strings.HasPrefix(r, dest+"/") || strings.HasPrefix(dest, r+"/")) {
				return nil, fmt.Errorf("%w: %s clashes with the branch %s", ErrConflict, to, strings.TrimPrefix(r, "refs/heads/"))
			}
		}
		zero := strings.Repeat("0", len(sha))
		if check != nil {
			if err := check(dest, "", sha); err != nil {
				return nil, err
			}
			if err := check(from, sha, zero); err != nil {
				return nil, err
			}
		}
		tip = sha
		return []storage.RefUpdate{{Name: dest, Old: zero, New: sha}, {Name: from, Old: sha, New: zero}}, nil
	})
	return tip, err
}
