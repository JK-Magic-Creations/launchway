package propose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

// Marker starts the machine-readable line at the end of a pull request body.
const Marker = "Updater record, please leave as it is: `launchway-dependency-update "

type marker struct {
	Key     string            `json:"key"`
	Targets map[string]string `json:"targets"`
}

// Body writes the pull request description in plain words. Everything in it
// is the updater's own text or comes from public registries; nothing is
// executed from it.
func Body(pr plan.Proposal, work Work, now time.Time, policy plan.Policy) string {
	var b strings.Builder
	if pr.Security {
		b.WriteString("This pull request fixes a known security problem in a dependency.\n\n")
	} else {
		b.WriteString("This pull request keeps dependencies up to date.\n\n")
	}
	b.WriteString("## What changes\n\n")
	for _, u := range pr.Updates {
		fmt.Fprintf(&b, "- `%s`: `%s` to `%s` (%s), in `%s`\n", u.Dep.Name, nz(u.Dep.Current, u.Dep.Constraint), u.Target, kindWord(u), u.Dep.File)
	}
	b.WriteString("\n## Why\n\n")
	found := false
	for _, u := range pr.Updates {
		if len(u.Advisories) == 0 {
			continue
		}
		found = true
		for _, a := range u.Advisories {
			line := fmt.Sprintf("- `%s %s` is affected by **%s** (%s severity)", u.Dep.Name, u.Dep.Current, a.ID, a.Severity)
			if a.Summary != "" {
				line += ": " + a.Summary
			}
			if a.FixedIn != "" {
				line += ". Fixed in " + a.FixedIn + "."
			}
			if a.URL != "" {
				line += " [Advisory](" + a.URL + ")"
			}
			b.WriteString(line + "\n")
		}
	}
	if found {
		b.WriteString("\nThis update is opened at once, outside the schedule, and is not held back by the wait for new releases.\n")
	} else {
		fmt.Fprintf(&b, "A routine update. Versions are proposed only after they have been public for %d days, so that a release that is withdrawn or found to be tampered with is not picked up at once.\n", policy.CooldownDays)
	}
	b.WriteString("\n## How risky is it\n\n")
	for _, k := range kinds(pr) {
		b.WriteString("- " + compat(k) + "\n")
	}
	var links []string
	for _, u := range pr.Updates {
		if u.NotesURL != "" {
			links = append(links, fmt.Sprintf("- `%s %s`: %s", u.Dep.Name, u.Target, u.NotesURL))
		}
	}
	if len(links) > 0 {
		b.WriteString("\n## Release notes\n\n" + strings.Join(links, "\n") + "\n")
	}
	b.WriteString("\n## What will be rebuilt\n\nThe pipeline runs on this pull request like on any other, with its normal checks. ")
	b.WriteString("Files changed: " + codeList(work.Files) + ".\n")
	if len(work.Steps) > 0 {
		if work.Isolated {
			b.WriteString("\nThe lock files were written by the ecosystem's own tool in an isolated container with no secrets, not edited by hand:\n")
		} else {
			b.WriteString("\nThe lock files were written by the ecosystem's own tool, not edited by hand (this run used the host executor of a runner, which does not isolate the tool):\n")
		}
		for _, s := range work.Steps {
			fmt.Fprintf(&b, "- `%s` in `%s`: %s\n", strings.Join(flat(s.Commands), " && "), s.Image, s.Why)
		}
	}
	if len(work.Notes) > 0 {
		b.WriteString("\n## Needs a person\n\n")
		for _, n := range work.Notes {
			b.WriteString("- " + n + "\n")
		}
	}
	b.WriteString("\n## About this pull request\n\nOpened by Launchway's dependency updater (" + groupWord(pr) + "). It keeps this one pull request for these dependencies and updates it in place when a newer version appears. ")
	b.WriteString("To stop, close it with a comment: `ignore this version`, `ignore this major` or `ignore this dependency`. To rebuild it on the current default branch, comment `@launchway rebase`; the updater picks that up on its next run.\n\n")
	m, _ := json.Marshal(marker{pr.Key, targets(pr)})
	b.WriteString(Marker + string(m) + "`\n")
	return b.String()
}

// ParseMarker reads the key and targets back from a pull request body.
func ParseMarker(body string) (key string, targets map[string]string, ok bool) {
	i := strings.LastIndex(body, Marker)
	if i < 0 {
		return "", nil, false
	}
	rest := body[i+len(Marker):]
	if j := strings.Index(rest, "`"); j >= 0 {
		rest = rest[:j]
	}
	var m marker
	if json.Unmarshal([]byte(rest), &m) != nil || m.Key == "" {
		return "", nil, false
	}
	return m.Key, m.Targets, true
}

func targets(pr plan.Proposal) map[string]string {
	m := map[string]string{}
	for _, u := range pr.Updates {
		m[u.Dep.Name] = u.Target
	}
	return m
}

func nz(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func kindWord(u plan.Update) string {
	switch u.Kind {
	case version.KindOther:
		return "rebuilt tag"
	case "":
		return "update"
	}
	return string(u.Kind)
}

func kinds(pr plan.Proposal) []version.Kind {
	set := map[version.Kind]bool{}
	for _, u := range pr.Updates {
		set[u.Kind] = true
	}
	var out []version.Kind
	for _, k := range []version.Kind{version.KindMajor, version.KindMinor, version.KindPatch, version.KindOther} {
		if set[k] {
			out = append(out, k)
		}
	}
	return out
}

func compat(k version.Kind) string {
	switch k {
	case version.KindMajor:
		return "**Major**: the maintainers may have changed or removed something. Read the release notes and expect to adjust code."
	case version.KindMinor:
		return "**Minor**: new features that should not break existing use."
	case version.KindPatch:
		return "**Patch**: bug and security fixes only; no change to how it is used."
	}
	return "**Rebuilt tag**: the same version number now points at a newer build of the image."
}

func groupWord(pr plan.Proposal) string {
	switch {
	case pr.Security:
		return "security update"
	case pr.Group != "":
		return "group " + pr.Group
	}
	return "single dependency"
}

func codeList(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	c := append([]string(nil), s...)
	sort.Strings(c)
	for i := range c {
		c[i] = "`" + c[i] + "`"
	}
	return strings.Join(c, ", ")
}

func flat(c [][]string) []string {
	var o []string
	for _, x := range c {
		o = append(o, strings.Join(x, " "))
	}
	return o
}

// CommitMessage is the commit of a proposal: the pull request title and a
// short body, in the repository's conventions.
func CommitMessage(pr plan.Proposal) string {
	var b strings.Builder
	b.WriteString(pr.Title + "\n\n")
	for _, u := range pr.Updates {
		fmt.Fprintf(&b, "- %s: %s -> %s\n", u.Dep.Name, nz(u.Dep.Current, u.Dep.Constraint), u.Target)
	}
	b.WriteString("\nOpened by Launchway's dependency updater.\n")
	return b.String()
}
