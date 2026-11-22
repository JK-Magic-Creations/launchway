package work

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReleaseNotes is the work finished between two moments, grouped by type, as
// the releases feature assembles it. It is a plain function so that anything
// may call it: the HTTP API, the command line, the MCP server, a release.
type ReleaseNotes struct {
	Scope    Scope               `json:"scope"`
	Since    string              `json:"since,omitempty"`
	Until    string              `json:"until,omitempty"`
	Items    []*Item             `json:"items"`
	Groups   map[string][]string `json:"groups"` // type -> refs, in order
	Markdown string              `json:"markdown"`
}

var groupTitles = map[string]string{TypeEpic: "Epics", TypeStory: "Stories", TypeBug: "Fixes", TypeIssue: "Issues", TypeTask: "Tasks"}

// ClosedSince returns the items finished (done, not "won't do") after since
// and up to until (zero is open ended) that the caller may see, and a Markdown
// list of them. Confidential items are left out unless the caller may see them.
func (s *Store) ClosedSince(ctx context.Context, c Caller, sc Scope, since, until time.Time) (*ReleaseNotes, error) {
	q := "state:done"
	if !since.IsZero() {
		q += " closed:>" + since.UTC().Format("2006-01-02")
	}
	pg, err := s.List(ctx, c, ListOptions{Scope: &sc, Query: q, Sort: "number", Limit: 500})
	if err != nil {
		return nil, err
	}
	rn := &ReleaseNotes{Scope: sc, Items: []*Item{}, Groups: map[string][]string{}}
	if !since.IsZero() {
		rn.Since = since.UTC().Format(time.RFC3339)
	}
	if !until.IsZero() {
		rn.Until = until.UTC().Format(time.RFC3339)
	}
	for _, it := range pg.Items {
		closed, _ := ParseTime(it.ClosedAt)
		if !since.IsZero() && closed <= since.UnixMilli() {
			continue
		}
		if !until.IsZero() && closed > until.UnixMilli() {
			continue
		}
		rn.Items = append(rn.Items, it)
		rn.Groups[it.Type] = append(rn.Groups[it.Type], it.Ref)
	}
	var b strings.Builder
	order := []string{TypeEpic, TypeStory, TypeBug, TypeIssue, TypeTask}
	for _, t := range order {
		var lines []string
		for _, it := range rn.Items {
			if it.Type == t {
				lines = append(lines, fmt.Sprintf("- %s (%s)", it.Title, it.Ref))
			}
		}
		sort.Strings(lines)
		if len(lines) > 0 {
			fmt.Fprintf(&b, "## %s\n\n%s\n\n", groupTitles[t], strings.Join(lines, "\n"))
		}
	}
	rn.Markdown = strings.TrimSpace(b.String())
	return rn, nil
}
