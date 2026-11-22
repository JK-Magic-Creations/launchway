package files

import (
	"bytes"
	"context"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// The languages of a repository by share: one bounded pass over the tree at a commit, files named by a small built-in
// extension map, vendored and generated paths left out by a short rule set, and both overridable by a file under
// .launchway/ in the repository (see LanguageRules). The result is a function of the commit and the rule file, so the
// caller keeps it by commit.

// MaxLanguageEntries bounds the pass: a tree with more files is counted up to this many and the answer says so.
const MaxLanguageEntries = 200000

// LanguagesFile is the attributes file that overrides the rules.
const LanguagesFile = ".launchway/languages"

// Language is one row of the answer.
type Language struct {
	Name  string  `json:"name"`
	Bytes int64   `json:"bytes"`
	Files int     `json:"files"`
	Share float64 `json:"share"` // percent of the counted bytes, two decimals
}

// LanguageReport is the languages of one commit.
type LanguageReport struct {
	Commit    string     `json:"commit"`
	Languages []Language `json:"languages"`
	// TotalBytes is the size of the files that were counted; Excluded is how many files the rules left out and
	// Unknown how many had no language in the map.
	TotalBytes int64 `json:"total_bytes"`
	Excluded   int   `json:"excluded_files"`
	Unknown    int   `json:"unknown_files"`
	// Truncated says the tree held more than MaxLanguageEntries files; the shares are of the first ones.
	Truncated bool `json:"truncated"`
	// Rules says where the rules came from: "built-in" or the attributes file.
	Rules string `json:"rules"`
}

var languageByExt = map[string]string{
	".go": "Go", ".py": "Python", ".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".java": "Java", ".kt": "Kotlin", ".kts": "Kotlin", ".rs": "Rust",
	".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".cxx": "C++", ".hpp": "C++", ".cs": "C#", ".rb": "Ruby",
	".php": "PHP", ".swift": "Swift", ".scala": "Scala", ".sh": "Shell", ".bash": "Shell", ".zsh": "Shell",
	".ps1": "PowerShell", ".lua": "Lua", ".pl": "Perl", ".r": "R", ".dart": "Dart", ".ex": "Elixir", ".exs": "Elixir",
	".erl": "Erlang", ".hs": "Haskell", ".clj": "Clojure", ".sql": "SQL", ".html": "HTML", ".htm": "HTML",
	".css": "CSS", ".scss": "SCSS", ".sass": "SCSS", ".less": "Less", ".vue": "Vue", ".svelte": "Svelte",
	".md": "Markdown", ".rst": "reStructuredText", ".tf": "HCL", ".hcl": "HCL", ".yaml": "YAML", ".yml": "YAML",
	".toml": "TOML", ".json": "JSON", ".proto": "Protocol Buffers", ".dockerfile": "Dockerfile", ".mk": "Makefile",
	".nix": "Nix", ".zig": "Zig", ".m": "Objective-C", ".vb": "Visual Basic",
}

var languageByName = map[string]string{"Dockerfile": "Dockerfile", "Makefile": "Makefile", "GNUmakefile": "Makefile", "Jenkinsfile": "Groovy"}

// Short rule sets: whole directories of other people's code, and files a tool wrote.
var vendoredDirs = []string{"vendor", "node_modules", "third_party", "third-party", "bower_components", "Pods", ".yarn", "__pycache__", "site-packages"}
var generatedNames = []string{"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "go.sum", "Cargo.lock", "composer.lock", "poetry.lock", "Gemfile.lock"}
var generatedSuffixes = []string{".min.js", ".min.css", ".pb.go", ".pb.cc", ".pb.h", "_pb2.py", ".generated.go", ".generated.ts", "_generated.go", ".map", ".snap"}

// LanguageRules are the overrides read from LanguagesFile: lines of "pattern exclude", "pattern include" and
// "pattern language=Name". A pattern with a slash is matched against the whole path, one without against the file name; a
// trailing slash or /** means everything below. The last line that matches a path decides.
type LanguageRules struct{ lines []langLine }

type langLine struct {
	pattern, action, language string
}

// ParseLanguageRules reads the attributes file; lines it cannot read are named in the second result.
func ParseLanguageRules(text string) (LanguageRules, []string) {
	var r LanguageRules
	var bad []string
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			bad = append(bad, "line "+strconv.Itoa(i+1))
			continue
		}
		l := langLine{pattern: f[0]}
		switch {
		case f[1] == "exclude" || f[1] == "include":
			l.action = f[1]
		case strings.HasPrefix(f[1], "language=") && len(f[1]) > len("language="):
			l.action, l.language = "language", strings.TrimPrefix(f[1], "language=")
		default:
			bad = append(bad, "line "+strconv.Itoa(i+1))
			continue
		}
		r.lines = append(r.lines, l)
	}
	return r, bad
}

func matchLanguagePattern(pat, p string) bool {
	if strings.HasSuffix(pat, "/**") {
		pat = strings.TrimSuffix(pat, "**")
	}
	if strings.HasSuffix(pat, "/") {
		dir := strings.TrimPrefix(pat, "/")
		return strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir)
	}
	if strings.Contains(pat, "/") {
		ok, _ := path.Match(strings.TrimPrefix(pat, "/"), p)
		return ok
	}
	ok, _ := path.Match(pat, path.Base(p))
	return ok
}

// classify says which language a path counts for, or "" when it does not count (excluded: it is a vendored or
// generated path, or a rule left it out; unknown: no language is known for it).
func (r LanguageRules) classify(p string) (lang string, excluded bool) {
	base := path.Base(p)
	skip := false
	for _, d := range vendoredDirs {
		if strings.HasPrefix(p, d+"/") || strings.Contains(p, "/"+d+"/") {
			skip = true
		}
	}
	for _, n := range generatedNames {
		if base == n {
			skip = true
		}
	}
	for _, s := range generatedSuffixes {
		if strings.HasSuffix(base, s) {
			skip = true
		}
	}
	forced := ""
	for _, l := range r.lines {
		if !matchLanguagePattern(l.pattern, p) {
			continue
		}
		switch l.action {
		case "exclude":
			skip, forced = true, ""
		case "include":
			skip = false
		case "language":
			skip, forced = false, l.language
		}
	}
	if skip {
		return "", true
	}
	if forced != "" {
		return forced, false
	}
	if n, ok := languageByName[base]; ok {
		return n, false
	}
	return languageByExt[strings.ToLower(path.Ext(base))], false
}

// treeWriter reads "git ls-tree -r -l -z" output as it comes and stops the command once enough entries are in.
type treeWriter struct {
	buf   []byte
	limit int
	n     int
	each  func(size int64, p string)
	// stopped says the limit ended the stream.
	stopped bool
}

var errEnough = errors.New("enough entries")

func (w *treeWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, 0)
		if i < 0 {
			return len(b), nil
		}
		rec := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type sha size
		if len(f) < 4 || f[1] != "blob" || f[0] == "120000" {
			continue
		}
		if w.n >= w.limit {
			w.stopped = true
			return 0, errEnough
		}
		w.n++
		size, _ := strconv.ParseInt(f[3], 10, 64)
		w.each(size, p)
	}
}

// Languages counts the languages of the files at a ref. Nothing is read but the tree and the rule file.
func (s *Service) Languages(ctx context.Context, repoID, ref string) (LanguageReport, error) {
	var rep LanguageReport
	err := s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		commit, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		rep = LanguageReport{Commit: commit, Languages: []Language{}, Rules: "built-in"}
		var rules LanguageRules
		if body, err := g.run(ctx, nil, "cat-file", "blob", "--end-of-options", commit+":"+LanguagesFile); err == nil && len(body) <= 64<<10 {
			rules, _ = ParseLanguageRules(body)
			rep.Rules = LanguagesFile
		}
		by := map[string]*Language{}
		w := &treeWriter{limit: MaxLanguageEntries, each: func(size int64, p string) {
			lang, excl := rules.classify(p)
			switch {
			case excl:
				rep.Excluded++
			case lang == "":
				rep.Unknown++
			default:
				l := by[lang]
				if l == nil {
					l = &Language{Name: lang}
					by[lang] = l
				}
				l.Bytes += size
				l.Files++
				rep.TotalBytes += size
			}
		}}
		_, err = gitexec.Run(ctx, gitexec.Spec{GitDir: g.dir, Args: []string{"ls-tree", "-r", "-l", "-z", "--end-of-options", commit}, Timeout: 2 * time.Minute, Limits: g.limits, Stdout: w})
		if err != nil {
			if !w.stopped {
				return err
			}
			rep.Truncated = true
		}
		for _, l := range by {
			if rep.TotalBytes > 0 {
				l.Share = float64(int64(float64(l.Bytes)/float64(rep.TotalBytes)*10000+0.5)) / 100
			}
			rep.Languages = append(rep.Languages, *l)
		}
		sort.Slice(rep.Languages, func(i, j int) bool {
			if rep.Languages[i].Bytes != rep.Languages[j].Bytes {
				return rep.Languages[i].Bytes > rep.Languages[j].Bytes
			}
			return rep.Languages[i].Name < rep.Languages[j].Name
		})
		return nil
	})
	return rep, err
}
