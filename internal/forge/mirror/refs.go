package mirror

import (
	"fmt"
	"sort"
	"strings"
)

// The source's pull request refs (refs/pull/N/head, ...) are kept here under
// refs/imported/pull/. refs/pull/ itself is this server's own, for the pull
// requests opened here, so the source's can not share it; and nothing may be
// pushed to refs/imported/ (see pulls.ReservedRef), so the history stays as
// it was at the source.
const (
	importedNS     = "refs/imported/"
	importedPullNS = importedNS + "pull/"
	sourcePullNS   = "refs/pull/"
)

// hostedName is the name here of a ref of the source, "" when the ref is
// not one that is copied.
func hostedName(sourceRef string) string {
	switch {
	case strings.HasPrefix(sourceRef, "refs/heads/"), strings.HasPrefix(sourceRef, "refs/tags/"), strings.HasPrefix(sourceRef, "refs/notes/"):
		return sourceRef
	case strings.HasPrefix(sourceRef, sourcePullNS):
		return importedPullNS + strings.TrimPrefix(sourceRef, sourcePullNS)
	}
	return ""
}

// namespaceOf groups a ref for the report: the part up to the second slash
// for the plain namespaces, "refs/imported/pull" for the source's pull refs.
func namespaceOf(ref string) string {
	if strings.HasPrefix(ref, importedPullNS) {
		return "refs/imported/pull"
	}
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) < 3 {
		return ref
	}
	return parts[0] + "/" + parts[1]
}

// parseSourceRefs reads `git ls-remote` output: the refs that are copied,
// under the names they have here, and how many refs of other namespaces
// there are (not copied).
func parseSourceRefs(out string) (refs map[string]string, notImported map[string]int) {
	refs, notImported = map[string]string{}, map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || strings.HasSuffix(name, "^{}") || name == "HEAD" {
			continue
		}
		if h := hostedName(name); h != "" {
			refs[h] = sha
		} else {
			notImported[namespaceOf(name)]++
		}
	}
	return refs, notImported
}

// Statuses of one ref in a verification.
const (
	RefEqual   = "equal"
	RefMissing = "missing"
	RefExtra   = "extra"
	RefDiffers = "differs"
)

// RefRow is one ref in a verification: the commit (or tag or note object) it
// names at the source and here.
type RefRow struct {
	Ref    string `json:"ref"`
	Source string `json:"source,omitempty"`
	Hosted string `json:"hosted,omitempty"`
	Status string `json:"status"`
}

func (r RefRow) describe() string {
	switch r.Status {
	case RefMissing:
		return r.Ref + ": missing here"
	case RefExtra:
		return r.Ref + ": not in the source"
	}
	return fmt.Sprintf("%s: source %.8s, here %.8s", r.Ref, r.Source, r.Hosted)
}

// compareRefs puts both sides' refs side by side, by name.
func compareRefs(theirs, ours map[string]string) []RefRow {
	names := make([]string, 0, len(theirs)+len(ours))
	for n := range theirs {
		names = append(names, n)
	}
	for n := range ours {
		if _, ok := theirs[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	rows := make([]RefRow, 0, len(names))
	for _, n := range names {
		a, inSource := theirs[n]
		b, inHosted := ours[n]
		row := RefRow{Ref: n, Source: a, Hosted: b, Status: RefEqual}
		switch {
		case inSource && !inHosted:
			row.Status = RefMissing
		case !inSource:
			row.Status = RefExtra
		case a != b:
			row.Status = RefDiffers
		}
		rows = append(rows, row)
	}
	return rows
}

// NamespaceCount is how many refs of one namespace each side has, and how
// many of them are equal.
type NamespaceCount struct {
	Namespace string `json:"namespace"`
	Source    int    `json:"source"`
	Hosted    int    `json:"hosted"`
	Equal     int    `json:"equal"`
}

// countNamespaces totals rows by namespace (heads, tags, notes, imported pull
// refs), in a fixed order.
func countNamespaces(rows []RefRow) []NamespaceCount {
	by := map[string]*NamespaceCount{}
	for _, ns := range []string{"refs/heads", "refs/tags", "refs/notes", "refs/imported/pull"} {
		by[ns] = &NamespaceCount{Namespace: ns}
	}
	for _, r := range rows {
		c := by[namespaceOf(r.Ref)]
		if c == nil {
			c = &NamespaceCount{Namespace: namespaceOf(r.Ref)}
			by[c.Namespace] = c
		}
		if r.Source != "" {
			c.Source++
		}
		if r.Hosted != "" {
			c.Hosted++
		}
		if r.Status == RefEqual {
			c.Equal++
		}
	}
	out := make([]NamespaceCount, 0, len(by))
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	return out
}
