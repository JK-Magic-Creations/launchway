package release

import (
	"fmt"
	"sort"
	"strings"
)

// Notes is generated release notes: the changes grouped, and the Markdown.
type Notes struct {
	Markdown     string              `json:"markdown"`
	Groups       map[string][]Change `json:"groups"`
	Contributors []string            `json:"contributors"`
	Count        int                 `json:"count"`
	Source       string              `json:"source"`
}

// Source supplies the changes since a tag. Two exist: the one that reads
// merged pull requests and commits (it works today), and, when the work
// tracker is present, one that supplies closed work items. The server uses
// the tracker's source if it is there and falls back to the first.
type Source interface {
	// Name says which source this is, for the notes' footer.
	Name() string
	// ChangesSince returns the changes between two commits of a project: from
	// (the previous release's commit; "" for the whole history) to (the
	// release's commit).
	ChangesSince(project, from, to string) ([]Change, error)
}

// ReleaseNotesSource is the interface the work tracker implements (it
// exposes "items closed since tag X"). It is called when present; the server
// holds none until that branch merges.
type ReleaseNotesSource = Source

// Group sorts changes into their sections, newest first inside each, and
// lists the people who made them.
func Group(changes []Change) (map[string][]Change, []string) {
	groups := map[string][]Change{}
	seen := map[string]bool{}
	var people []string
	for _, c := range changes {
		c.Section, _ = Classify(c)
		groups[c.Section] = append(groups[c.Section], c)
		if a := strings.TrimSpace(c.Author); a != "" && !seen[a] && !strings.HasSuffix(a, "[bot]") {
			seen[a] = true
			people = append(people, a)
		}
	}
	sort.Strings(people)
	return groups, people
}

// Render writes Markdown for a version. link may be nil. Titles are taken as
// they are; the viewers render Markdown through the product's sanitizer.
func Render(version string, changes []Change, source string) Notes {
	groups, people := Group(changes)
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", version)
	if len(changes) == 0 {
		b.WriteString("No changes were found since the previous release.\n")
	}
	for _, s := range Sections {
		list := groups[s.ID]
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", s.Heading)
		for _, c := range list {
			_, _, _, title := ParseSubject(c.Title)
			if title == "" {
				title = c.Title
			}
			title = oneLine(title)
			fmt.Fprintf(&b, "- %s", title)
			switch {
			case c.Kind == "pull" && c.Ref > 0:
				fmt.Fprintf(&b, " (%s)", link("#"+fmt.Sprint(c.Ref), c.URL))
			case c.Kind == "item" && c.Ref > 0:
				fmt.Fprintf(&b, " (%s)", link("#"+fmt.Sprint(c.Ref), c.URL))
			case c.SHA != "":
				fmt.Fprintf(&b, " (%s)", link(c.SHA[:min(7, len(c.SHA))], c.URL))
			}
			if c.Author != "" {
				fmt.Fprintf(&b, " by %s", oneLine(c.Author))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(people) > 0 {
		b.WriteString("### Contributors\n\n" + strings.Join(people, ", ") + "\n")
	}
	return Notes{Markdown: strings.TrimRight(b.String(), "\n") + "\n", Groups: groups, Contributors: people, Count: len(changes), Source: source}
}

func link(text, url string) string {
	if url == "" || !(strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) || strings.ContainsAny(url, " ()<>") {
		return text
	}
	return "[" + text + "](" + url + ")"
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ChangelogEntry places a release's notes at the top of a changelog file's
// text, under its title, and returns the new text. Existing text is kept
// as it is; a version already there is refused.
func ChangelogEntry(existing, version, notes string) (string, error) {
	head := "## " + version
	if strings.Contains(existing, "\n"+head+"\n") || strings.HasPrefix(existing, head+"\n") {
		return "", fmt.Errorf("the changelog already has an entry for %s", version)
	}
	notes = strings.TrimRight(notes, "\n") + "\n"
	if !strings.HasPrefix(strings.TrimSpace(notes), "## ") {
		notes = head + "\n\n" + notes
	}
	if strings.TrimSpace(existing) == "" {
		return "# Changelog\n\n" + notes, nil
	}
	if i := strings.Index(existing, "\n## "); i >= 0 && strings.HasPrefix(existing, "#") && !strings.HasPrefix(existing, "## ") {
		return existing[:i+1] + notes + "\n" + existing[i+1:], nil
	}
	if strings.HasPrefix(existing, "## ") {
		return notes + "\n" + existing, nil
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + notes, nil
}
