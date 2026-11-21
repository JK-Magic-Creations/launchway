// Package ansible reads the run report Launchway's Ansible Maneuver writes
// (the launchway_report callback, see internal/starters/library/ansible) and
// turns it into what the product shows: a hosts-by-plays matrix, a "what
// changed" summary, the failed task first, a recap line, the hosts to run
// again, and, for a check-mode run, the drift per host.
//
// The tool itself runs in a step container; nothing here runs Ansible. The
// report is data from a host the owner does not fully control, so it is read
// with limits and redacted again on the way in: a task marked no_log never
// has its message or diff in the stored report, and anything that looks like
// a credential is replaced before it is stored or shown.
package ansible

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Format is the report format the callback writes.
const Format = "launchway-ansible-report/1"

// Limits on what is read.
const (
	MaxReportBytes = 32 << 20
	MaxTasks       = 20000
	MaxHosts       = 5000
	MaxDiffBytes   = 20000
	MaxMsgBytes    = 4000
	Redacted       = "[redacted]"
)

// Statuses of one host's result of one task.
const (
	StatusOK          = "ok"
	StatusChanged     = "changed"
	StatusFailed      = "failed"
	StatusSkipped     = "skipped"
	StatusUnreachable = "unreachable"
)

// Host outcomes: the worst thing that happened to the host in the run.
const (
	HostFailed      = "failed"
	HostUnreachable = "unreachable"
	HostChanged     = "changed"
	HostOK          = "ok"
)

// Report is one playbook run.
type Report struct {
	Format         string           `json:"format"`
	AnsibleVersion string           `json:"ansible_version,omitempty"`
	Playbook       string           `json:"playbook,omitempty"`
	Environment    string           `json:"environment,omitempty"`
	Check          bool             `json:"check"`
	Diff           bool             `json:"diff"`
	DurationMs     int64            `json:"duration_ms"`
	Plays          []Play           `json:"plays"`
	Stats          map[string]Stats `json:"stats"`
	Hosts          []HostSummary    `json:"hosts"`
	Summary        Summary          `json:"summary"`
	Failure        *Failure         `json:"failure,omitempty"`
	Notes          []string         `json:"notes,omitempty"`
}

// Play is one play of the playbook.
type Play struct {
	Name  string `json:"name"`
	Tasks []Task `json:"tasks"`
}

// Task is one task, with each host's result of it.
type Task struct {
	Name    string            `json:"name"`
	Action  string            `json:"action,omitempty"`
	Handler bool              `json:"handler,omitempty"`
	NoLog   bool              `json:"no_log,omitempty"`
	Results map[string]Result `json:"results"`
}

// Result is what one task did on one host.
type Result struct {
	Status     string `json:"status"`
	Changed    bool   `json:"changed,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Msg        string `json:"msg,omitempty"`
	Ignored    bool   `json:"ignored,omitempty"`
	Diff       []Diff `json:"diff,omitempty"`
}

// Diff is one change in diff mode.
type Diff struct {
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Text   string `json:"text"`
}

// Stats is Ansible's own recap of one host.
type Stats struct {
	OK          int `json:"ok"`
	Changed     int `json:"changed"`
	Failures    int `json:"failures"`
	Unreachable int `json:"unreachable"`
	Skipped     int `json:"skipped"`
	Rescued     int `json:"rescued"`
	Ignored     int `json:"ignored"`
}

// HostSummary is one host's outcome across the run.
type HostSummary struct {
	Host    string `json:"host"`
	Outcome string `json:"outcome"`
	Stats
	// ChangedTasks names the tasks that changed (or, in check mode, would
	// change) the host, in order.
	ChangedTasks []string `json:"changed_tasks,omitempty"`
}

// Summary counts the hosts by outcome.
type Summary struct {
	Hosts       int    `json:"hosts"`
	Changed     int    `json:"changed"`
	OK          int    `json:"ok"`
	Failed      int    `json:"failed"`
	Unreachable int    `json:"unreachable"`
	Recap       string `json:"recap"`
}

// Failure is the first task that failed (or could not reach a host), the
// thing a person wants to see before anything else.
type Failure struct {
	Play   string `json:"play"`
	Task   string `json:"task"`
	Host   string `json:"host"`
	Status string `json:"status"`
	Msg    string `json:"msg"`
}

// Parse reads a report. secrets are values the caller knows must not appear
// (a value the run was given); they are removed from every text, in addition
// to the patterns Redact knows.
func Parse(data []byte, secrets ...string) (*Report, error) {
	if len(data) > MaxReportBytes {
		return nil, fmt.Errorf("the run report is %d bytes; at most %d are read", len(data), MaxReportBytes)
	}
	var r Report
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("the run report is not valid JSON: %w", err)
	}
	if r.Format != Format {
		return nil, fmt.Errorf("the run report has format %q; this server reads %q", r.Format, Format)
	}
	if r.Stats == nil {
		r.Stats = map[string]Stats{}
	}
	red := newRedactor(secrets)
	tasks := 0
	hosts := map[string]bool{}
	for pi := range r.Plays {
		p := &r.Plays[pi]
		p.Name = red.text(p.Name)
		for ti := range p.Tasks {
			t := &p.Tasks[ti]
			if tasks++; tasks > MaxTasks {
				return nil, errors.New("the run report has too many tasks to read")
			}
			t.Name = red.text(t.Name)
			if t.Results == nil {
				t.Results = map[string]Result{}
			}
			for h, res := range t.Results {
				if hosts[h] = true; len(hosts) > MaxHosts {
					return nil, errors.New("the run report has too many hosts to read")
				}
				if !validStatus(res.Status) {
					return nil, fmt.Errorf("task %q, host %q: unknown status %q", t.Name, h, res.Status)
				}
				if t.NoLog {
					// The callback already dropped these; do not trust it.
					res.Diff = nil
					if res.Msg != "" {
						res.Msg = "(hidden: the task is marked no_log)"
					}
				}
				res.Msg = red.text(clip(res.Msg, MaxMsgBytes))
				for i := range res.Diff {
					res.Diff[i].Text = red.text(clip(res.Diff[i].Text, MaxDiffBytes))
				}
				t.Results[h] = res
			}
		}
	}
	for h := range r.Stats {
		hosts[h] = true
	}
	r.derive(hosts)
	return &r, nil
}

func validStatus(s string) bool {
	switch s {
	case StatusOK, StatusChanged, StatusFailed, StatusSkipped, StatusUnreachable:
		return true
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n(cut)"
}

// derive fills hosts, summary, failure and counts.
func (r *Report) derive(hosts map[string]bool) {
	byHost := map[string]*HostSummary{}
	for h := range hosts {
		hs := &HostSummary{Host: h, Stats: r.Stats[h]}
		byHost[h] = hs
	}
	for pi := range r.Plays {
		for _, t := range r.Plays[pi].Tasks {
			names := make([]string, 0, len(t.Results))
			for h := range t.Results {
				names = append(names, h)
			}
			sort.Strings(names)
			for _, h := range names {
				res := t.Results[h]
				hs := byHost[h]
				switch res.Status {
				case StatusChanged:
					hs.ChangedTasks = append(hs.ChangedTasks, t.Name)
				case StatusFailed, StatusUnreachable:
					if r.Failure == nil && !res.Ignored {
						r.Failure = &Failure{Play: r.Plays[pi].Name, Task: t.Name, Host: h, Status: res.Status, Msg: res.Msg}
					}
				}
			}
		}
	}
	for _, hs := range byHost {
		switch {
		case hs.Failures > 0 || hasStatus(r, hs.Host, StatusFailed):
			hs.Outcome = HostFailed
		case hs.Unreachable > 0 || hasStatus(r, hs.Host, StatusUnreachable):
			hs.Outcome = HostUnreachable
		case hs.Changed > 0 || len(hs.ChangedTasks) > 0:
			hs.Outcome = HostChanged
		default:
			hs.Outcome = HostOK
		}
		r.Hosts = append(r.Hosts, *hs)
	}
	sort.Slice(r.Hosts, func(i, j int) bool { return r.Hosts[i].Host < r.Hosts[j].Host })
	s := Summary{Hosts: len(r.Hosts)}
	for _, h := range r.Hosts {
		switch h.Outcome {
		case HostFailed:
			s.Failed++
		case HostUnreachable:
			s.Unreachable++
		case HostChanged:
			s.Changed++
		default:
			s.OK++
		}
	}
	s.Recap = recap(s, r.Check)
	r.Summary = s
}

func hasStatus(r *Report, host, status string) bool {
	for _, p := range r.Plays {
		for _, t := range p.Tasks {
			if res, ok := t.Results[host]; ok && res.Status == status && !res.Ignored {
				return true
			}
		}
	}
	return false
}

// recap is the line on the launch line: "12 hosts: 9 changed, 3 ok, 0 failed".
// A check-mode run says "would change", because it changed nothing.
func recap(s Summary, check bool) string {
	noun := "hosts"
	if s.Hosts == 1 {
		noun = "host"
	}
	changed := "changed"
	if check {
		changed = "would change"
	}
	out := fmt.Sprintf("%d %s: %d %s, %d ok, %d failed", s.Hosts, noun, s.Changed, changed, s.OK, s.Failed)
	if s.Unreachable > 0 {
		out += fmt.Sprintf(", %d unreachable", s.Unreachable)
	}
	return out
}

// FailedHosts are the hosts to run again: those that failed or could not be
// reached, in order.
func (r *Report) FailedHosts() []string {
	var out []string
	for _, h := range r.Hosts {
		if h.Outcome == HostFailed || h.Outcome == HostUnreachable {
			out = append(out, h.Host)
		}
	}
	return out
}

// Drift is what a check-mode run found: the hosts whose configuration is
// not what the code says.
type Drift struct {
	Drifted int         `json:"drifted"`
	Hosts   int         `json:"hosts"`
	Items   []DriftHost `json:"items"`
	Text    string      `json:"text"`
	Clean   bool        `json:"clean"`
}

// DriftHost is one host's differences.
type DriftHost struct {
	Host  string   `json:"host"`
	Tasks []string `json:"tasks"`
}

// Drift summarises a check-mode report. A report that was not a check-mode
// run has no drift to report and says so rather than guess.
func (r *Report) Drift() (Drift, bool) {
	if !r.Check {
		return Drift{}, false
	}
	d := Drift{Hosts: len(r.Hosts), Items: []DriftHost{}}
	names := map[string]bool{}
	for _, h := range r.Hosts {
		if h.Outcome == HostChanged {
			d.Drifted++
			d.Items = append(d.Items, DriftHost{Host: h.Host, Tasks: dedupe(h.ChangedTasks)})
			for _, t := range h.ChangedTasks {
				names[t] = true
			}
		}
	}
	d.Clean = d.Drifted == 0 && r.Summary.Failed == 0 && r.Summary.Unreachable == 0
	list := make([]string, 0, len(names))
	for n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	switch {
	case d.Drifted > 0:
		if len(list) > 4 {
			list = append(list[:4], fmt.Sprintf("and %d more", len(names)-4))
		}
		noun := "hosts"
		if d.Drifted == 1 {
			noun = "host"
		}
		d.Text = fmt.Sprintf("%d %s drifted: %s", d.Drifted, noun, strings.Join(list, ", "))
	case d.Clean:
		d.Text = "no drift: the hosts match the code"
	default:
		d.Text = "the check could not finish on every host: " + r.Summary.Recap
	}
	return d, true
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- redaction ----

type redactor struct {
	literal []string
}

func newRedactor(secrets []string) *redactor {
	r := &redactor{}
	for _, s := range secrets {
		if len(s) >= 4 {
			r.literal = append(r.literal, s)
		}
	}
	// Longest first, so a secret that contains another is removed whole.
	sort.Slice(r.literal, func(i, j int) bool { return len(r.literal[i]) > len(r.literal[j]) })
	return r
}

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)(["']?\s*[:=]\s*["']?)[^\s"',;}]{3,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`\$ANSIBLE_VAULT;[0-9.]+;AES256\s*[0-9a-f\s]+`),
}

func (r *redactor) text(s string) string {
	if s == "" {
		return s
	}
	for _, l := range r.literal {
		s = strings.ReplaceAll(s, l, Redacted)
	}
	for i, p := range patterns {
		switch i {
		case 1:
			s = p.ReplaceAllString(s, "${1}${2}"+Redacted)
		case 5:
			s = p.ReplaceAllString(s, "${1}"+Redacted+"@")
		default:
			s = p.ReplaceAllString(s, Redacted)
		}
	}
	return s
}

// Redact removes credentials from a text the way Parse does; it is exported
// for the other places that show a tool's output.
func Redact(s string, secrets ...string) string { return newRedactor(secrets).text(s) }
