package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Limits on a failed step's excerpt, as stored. The runner sends less; these
// protect the database and the UI from a runner that does not.
const (
	MaxExcerptLines = 8
	MaxExcerptBytes = 2000
)

// ansi matches terminal escape sequences (colours, cursor movement).
var ansi = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// CleanExcerpt makes a failed step's last lines safe to store and show: valid
// text without terminal escapes or control characters, no empty lines, at
// most MaxExcerptLines lines and MaxExcerptBytes bytes, keeping the end,
// which is where the reason usually is.
func CleanExcerpt(s string) string {
	s = ansi.ReplaceAllString(strings.ToValidUTF8(s, "?"), "")
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimRightFunc(strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, ln), unicode.IsSpace)
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) > MaxExcerptLines {
		lines = lines[len(lines)-MaxExcerptLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > MaxExcerptBytes {
		cut := len(out) - MaxExcerptBytes
		for cut < len(out) && !utf8.RuneStart(out[cut]) {
			cut++
		}
		out = "…" + out[cut:]
	}
	return out
}

// draftSecretsRefused explains why a step of a draft run failed without
// running.
func draftSecretsRefused(project string, def *pipeline.Step) string {
	names := slices.Sorted(maps.Keys(def.Secrets))
	return fmt.Sprintf("not run: this is a draft run, and draft runs of %s may not read secrets (the step asks for %s). "+
		"Save the pipeline and run it from its branch, or ask an administrator to set drafts.secrets: true for the project",
		project, strings.Join(names, ", "))
}

// pullRequestSecretsRefused explains why a step of a pull-request run failed
// without running.
func pullRequestSecretsRefused(st *RunState, def *pipeline.Step) string {
	project := st.Project
	names := slices.Sorted(maps.Keys(def.Secrets))
	if st.restricted() {
		return fmt.Sprintf("not run: this run is restricted (%s), so it may not read secrets (the step asks for %s). A maintainer can approve it with Run with secrets",
			st.Restriction.Reason, strings.Join(names, ", "))
	}
	return fmt.Sprintf("not run: this run is for a pull request, whose code may come from anyone who can open one, and pull-request runs of %s "+
		"may not read secrets (the step asks for %s). Guard the step with when: { event: [push, manual] }, or ask an administrator "+
		"to set pull_requests.secrets: true for the project",
		project, strings.Join(names, ", "))
}

// DraftText returns the pipeline text a draft run ran, and its hash. ok is
// false for a run that is not a draft.
func (e *Engine) DraftText(runID string) (d Draft, ok bool, err error) {
	var data string
	err = e.db.QueryRow(`SELECT data FROM events WHERE run_id = ? AND type = ? ORDER BY seq LIMIT 1`, runID, RunCreated).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, false, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err != nil {
		return Draft{}, false, err
	}
	var created Payload
	if err := json.Unmarshal([]byte(data), &created); err != nil {
		return Draft{}, false, err
	}
	if created.Draft == nil {
		return Draft{}, false, nil
	}
	return *created.Draft, true, nil
}
