package pulls

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// FileChange is one file of a comparison.
type FileChange struct {
	Path string `json:"path"`
	// OldPath is the path before, for a rename or a copy.
	OldPath string `json:"old_path,omitempty"`
	// Status is added, modified, deleted, renamed, copied or typechange.
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	// Similarity is how much of a renamed file is unchanged, in percent.
	Similarity int `json:"similarity,omitempty"`
	// TooLarge says the patch is over the limits and is served only when
	// asked for ("load anyway").
	TooLarge bool `json:"too_large,omitempty"`
}

// Limits bound what a comparison serves. A patch over a limit is not sent
// unless the caller asks for the whole of it, and even then the hard ceiling
// holds.
type Limits struct {
	// MaxFiles is how many files a comparison lists at most.
	MaxFiles int
	// MaxPatchBytes is how large one file's patch may be before it is held
	// back; MaxDiffBytes is the same for the unified patch of everything.
	MaxPatchBytes, MaxDiffBytes int
	// MaxPatchLines holds back a file with more changed lines.
	MaxPatchLines int
	// HardBytes is the most any single answer carries, "load anyway" or not.
	HardBytes int
}

// DefaultLimits are what a comparison serves without being asked for more.
var DefaultLimits = Limits{MaxFiles: 3000, MaxPatchBytes: 256 << 10, MaxDiffBytes: 1 << 20, MaxPatchLines: 5000, HardBytes: 16 << 20}

// Comparison is the difference between two commits.
type Comparison struct {
	From  string       `json:"from"`
	To    string       `json:"to"`
	Files []FileChange `json:"files"`
	// Total is how many files changed (it can exceed len(Files) when paged).
	Total     int `json:"total"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	// Truncated says the list was cut at the limit of files.
	Truncated bool `json:"truncated,omitempty"`
}

// Patch is a patch of one file or of everything.
type Patch struct {
	Path string `json:"path,omitempty"`
	Diff string `json:"diff"`
	// Truncated says Diff is the start of the patch: it was over the limit.
	Truncated bool `json:"truncated,omitempty"`
	// Held says the patch was not read: it is over the default limit and the
	// caller did not ask to load it anyway.
	Held   bool `json:"held,omitempty"`
	Binary bool `json:"binary,omitempty"`
	Bytes  int  `json:"bytes"`
}

func statusName(c byte) string {
	switch c {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "typechange"
	}
	return "modified"
}

// compare lists what changed from one commit to another, with rename
// detection, line counts and binary files, paged.
func (g gitRunner) compare(ctx context.Context, from, to string, lim Limits) (Comparison, error) {
	c := Comparison{From: from, To: to, Files: []FileChange{}}
	raw, err := g.raw(ctx, "", "diff", "--raw", "-z", "-M", "--no-ext-diff", "--no-textconv", "--no-color", from, to)
	if err != nil {
		return c, err
	}
	num, err := g.raw(ctx, "", "diff", "--numstat", "-z", "-M", "--no-ext-diff", "--no-textconv", "--no-color", from, to)
	if err != nil {
		return c, err
	}
	type counts struct {
		add, del int
		bin      bool
	}
	byPath := map[string]counts{}
	f := strings.Split(num, "\x00")
	for i := 0; i < len(f); i++ {
		if f[i] == "" {
			continue
		}
		a, d, p, ok := splitNum(f[i])
		if !ok {
			continue
		}
		path := p
		if p == "" && i+2 < len(f) { // a rename: the two paths follow
			path = f[i+2]
			i += 2
		}
		cs := counts{}
		if a == "-" || d == "-" {
			cs.bin = true
		} else {
			cs.add, _ = strconv.Atoi(a)
			cs.del, _ = strconv.Atoi(d)
		}
		byPath[path] = cs
	}
	r := strings.Split(raw, "\x00")
	for i := 0; i < len(r); i++ {
		h := r[i]
		if !strings.HasPrefix(h, ":") {
			continue
		}
		fields := strings.Fields(h)
		if len(fields) < 5 || len(fields[4]) == 0 {
			continue
		}
		st := fields[4]
		fc := FileChange{Status: statusName(st[0])}
		if i+1 >= len(r) {
			break
		}
		if st[0] == 'R' || st[0] == 'C' {
			if i+2 >= len(r) {
				break
			}
			fc.OldPath, fc.Path = r[i+1], r[i+2]
			fc.Similarity, _ = strconv.Atoi(st[1:])
			i += 2
		} else {
			fc.Path = r[i+1]
			i++
		}
		cs := byPath[fc.Path]
		fc.Additions, fc.Deletions, fc.Binary = cs.add, cs.del, cs.bin
		fc.TooLarge = !fc.Binary && (fc.Additions+fc.Deletions > lim.MaxPatchLines)
		c.Additions += fc.Additions
		c.Deletions += fc.Deletions
		c.Total++
		if len(c.Files) < lim.MaxFiles {
			c.Files = append(c.Files, fc)
		} else {
			c.Truncated = true
		}
	}
	return c, nil
}

func splitNum(s string) (a, d, p string, ok bool) {
	a, rest, ok := strings.Cut(s, "\t")
	if !ok {
		return
	}
	d, p, ok = strings.Cut(rest, "\t")
	return
}

// patch returns the unified patch from one commit to another; with paths it
// is restricted to them (a renamed file needs both its names).
func (g gitRunner) patch(ctx context.Context, from, to string, paths []string, maxBytes int) (Patch, error) {
	args := []string{"diff", "-M", "--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "-U3", from, to}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	var buf limitedBuffer
	buf.max = maxBytes
	sp := g.spec(args...)
	sp.Stdout = &buf
	sp.Env = append(append([]string{}, sp.Env...), "GIT_LITERAL_PATHSPECS=1")
	sp.Timeout = 2 * time.Minute
	if _, err := gitexec.Run(ctx, sp); err != nil && !buf.full {
		return Patch{}, gitErr(args, err)
	}
	p := Patch{Diff: buf.String(), Truncated: buf.full, Bytes: buf.Len()}
	if len(paths) > 0 {
		p.Path = paths[len(paths)-1]
	}
	return p, nil
}

// limitedBuffer keeps the start of what is written and discards the rest,
// without failing the writer: git is allowed to finish (or be stopped by its
// timeout), the answer is cut.
type limitedBuffer struct {
	bytes.Buffer
	max  int
	full bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		b.full = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// CommitInfo is a commit of a pull request.
type CommitInfo struct {
	SHA     string    `json:"sha"`
	Parents []string  `json:"parents"`
	Author  string    `json:"author"`
	Email   string    `json:"author_email"`
	At      time.Time `json:"authored_at"`
	Subject string    `json:"subject"`
	Body    string    `json:"body,omitempty"`
}

// commits lists the commits reachable from head and not from base, oldest
// first, at most limit.
func (g gitRunner) commits(ctx context.Context, base, head string, limit int) ([]CommitInfo, error) {
	const sep = "\x1e"
	out, err := g.raw(ctx, "", "log", "--reverse", "--topo-order", fmt.Sprintf("--max-count=%d", limit), "--no-color",
		"--format=%H%x00%P%x00%an%x00%ae%x00%at%x00%s%x00%b"+sep, head, "^"+base)
	if err != nil {
		return nil, err
	}
	res := []CommitInfo{}
	for _, rec := range strings.Split(out, sep) {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x00", 7)
		if len(f) < 7 {
			continue
		}
		at, _ := strconv.ParseInt(f[4], 10, 64)
		ci := CommitInfo{SHA: f[0], Author: f[2], Email: f[3], At: time.Unix(at, 0).UTC(), Subject: f[5], Body: strings.TrimSpace(f[6])}
		if f[1] != "" {
			ci.Parents = strings.Fields(f[1])
		}
		res = append(res, ci)
	}
	return res, nil
}
