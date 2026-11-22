package hosts

import (
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// The capability matrix: what each registered host kind can do, generated
// from the providers' own declarations (Entry.Caps), never written by hand.
// It reveals no credential or connection, only what the product itself says.

// MatrixFeature names one row of the matrix.
type MatrixFeature struct {
	Key   forge.Capability `json:"key"`
	Title string           `json:"title"`
}

// MatrixKind is one column: a host kind with its declarations.
type MatrixKind struct {
	Kind         forge.Kind                             `json:"kind"`
	Label        string                                 `json:"label"`
	Capabilities map[forge.Capability]forge.Declaration `json:"capabilities"`
	Additions    []forge.Addition                       `json:"additions,omitempty"`
}

// MatrixView is the whole matrix, features in a fixed order and kinds sorted.
type MatrixView struct {
	Features []MatrixFeature `json:"features"`
	Kinds    []MatrixKind    `json:"kinds"`
}

// featureTitles say each capability in a few plain words.
var featureTitles = map[forge.Capability]string{
	forge.CapIdentity:     "Check who the token is",
	forge.CapRepositories: "List and choose repositories",
	forge.CapRepoCreate:   "Create repositories",
	forge.CapContent:      "Read code",
	forge.CapEvents:       "Start a Sortie on a push, tag or pull request",
	forge.CapAccess:       "Check a person's access",
	forge.CapProposals:    "Pull requests",
	forge.CapProposalRef:  "Fetch a pull request head by a ref",
	forge.CapDrafts:       "Draft pull requests",
	forge.CapReviews:      "Reviews",
	forge.CapResults:      "Report a result on a commit",
	forge.CapResultsGate:  "Results that gate a merge",
	forge.CapLand:         "Merge a pull request",
	forge.CapAutoLand:     "Merge when checks pass",
	forge.CapRules:        "Read branch rules",
	forge.CapForks:        "Forks",
	forge.CapLFS:          "Large file storage",
	forge.CapTags:         "Write tags",
	forge.CapReleases:     "Publish releases",
	forge.CapCommitFile:   "Commit a file change",
	forge.CapProposalNote: "One updated summary comment on a pull request",
}

// Matrix builds the matrix from every registered kind that declares itself.
func Matrix() MatrixView {
	v := MatrixView{Kinds: []MatrixKind{}}
	for _, c := range forge.AllCapabilities {
		t := featureTitles[c]
		if t == "" {
			t = string(c)
		}
		v.Features = append(v.Features, MatrixFeature{Key: c, Title: t})
	}
	for _, k := range Kinds() {
		e, _ := entryOf(k)
		if e.Caps == nil {
			continue
		}
		caps, adds := e.Caps()
		v.Kinds = append(v.Kinds, MatrixKind{Kind: k, Label: e.Label, Capabilities: caps, Additions: adds})
	}
	return v
}

// Markdown renders the matrix as the guide page docs/guide/outside-host-matrix.md.
// The output is deterministic: the page is compared to it by a test.
func (v MatrixView) Markdown() string {
	var b strings.Builder
	b.WriteString("# What each outside host can do\n\n")
	b.WriteString("<!-- Generated from the providers' own declarations. Do not edit by hand: run `make docs-matrix`. -->\n\n")
	b.WriteString("Every host kind says for each feature whether it is **native** (the host does it), **emulated** (Launchway does it another way) or **none** (it cannot be done, with what to do instead). This page is generated from those declarations, so it cannot disagree with the product. The same data is served by `GET /api/v1/outside-host-matrix` and `launchway repos host matrix`. See [Keep your code on another host](outside-host.md) for connecting one.\n\n")
	b.WriteString("## At a glance\n\n| Feature |")
	for _, k := range v.Kinds {
		b.WriteString(" " + k.Label + " |")
	}
	b.WriteString("\n| --- |")
	for range v.Kinds {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, f := range v.Features {
		b.WriteString("| " + f.Title + " |")
		for _, k := range v.Kinds {
			b.WriteString(" " + string(k.Capabilities[f.Key].Support) + " |")
		}
		b.WriteString("\n")
	}
	for _, k := range v.Kinds {
		fmt.Fprintf(&b, "\n## %s\n\nKind `%s`.\n", k.Label, k.Kind)
		var lines []string
		for _, f := range v.Features {
			d := k.Capabilities[f.Key]
			if d.Support == forge.Native {
				continue
			}
			lines = append(lines, fmt.Sprintf("- **%s** (%s): %s Instead: %s", f.Title, d.Support, sentence(d.Reason), sentence(d.Workaround)))
		}
		if len(lines) == 0 {
			b.WriteString("\nEvery feature is native.\n")
		} else {
			b.WriteString("\nNot native:\n\n" + strings.Join(lines, "\n") + "\n")
		}
		if len(k.Additions) > 0 {
			b.WriteString("\nWhat the host adds:\n\n")
			for _, a := range k.Additions {
				how := "used on the host"
				if a.Drivable {
					how = "Launchway can switch it on"
				}
				fmt.Fprintf(&b, "- **%s** (%s): %s\n", a.Title, how, sentence(a.What))
			}
		}
	}
	return b.String()
}

// sentence ends a text with a full stop, so lists read the same everywhere.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}
