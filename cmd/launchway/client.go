package main

// The command line's client of a server's API: launchway login and
// launchway runs. It authenticates with an API token, made in the web UI
// (API tokens), which login stores in a file only its owner can read.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// Exit codes of the client commands. With --wait, runs start exits with the
// run's outcome.
const (
	exitRunFailed   = 1 // the run failed
	exitUsage       = 2 // the command was written wrongly
	exitRunCanceled = 3 // the run was canceled
	exitAPI         = 4 // the server refused, or could not be reached
	exitTimeout     = 5 // --timeout passed before the run finished; it goes on
)

var clientUsage = themed(`Usage:
  launchway login --server URL              store an API token for the server (read from
                  [--allow-http]            LAUNCHWAY_TOKEN, or typed or piped in)
  launchway runs start <project> [--ref R] [-i name=value ...] [--wait] [--timeout D] [--json]
  launchway runs list [--project P] [--status S] [--lane NAME] [--limit N] [--json]
  launchway runs show <run-id> [--json]
  launchway runs logs <run-id> [step] [--step S] [--service NAME] [--follow]   --service: a service container's output
  launchway runs cancel <run-id>
  launchway approvals list|approve|reject          see launchway approvals
  launchway runs resume <run-id> [--wait] [--timeout D] [--json]       the same {run} goes on, running its failed steps again
  launchway runs rerun <run-id> [--from STEP]... [--failed] [--plan] [--wait] [--timeout D] [--json]
                                                          a new {run} from those steps (or the failed ones), reusing the rest
  launchway runs queue [project] [--json]          runs held back by limits, in order, with why; next|hold|release <run-id> moves one
  launchway runs trust <run-id> [--comment text]   approve a restricted run (a pull request from an automation account): Run with secrets
  launchway runs lanes <project> [--json]          each lane: the {run} that holds it, and the {runs} waiting
  launchway runs tests <run-id> [--all | --status S] [--tag T] [--json]   failures first, with messages
  launchway runs coverage <run-id> [--files] [--json]
  launchway runs reports <run-id> [--json]                links to its HTML reports

LAUNCHWAY_SERVER and LAUNCHWAY_TOKEN, if set, are used instead of what login
stored. Exit codes: 0 success; 1 the run failed; 2 usage; 3 the run was
canceled; 4 the server refused or could not be reached; 5 --timeout passed.
`)

// credentials are what login stores.
type credentials struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// credentialsPath is where login stores the server and token:
// $LAUNCHWAY_CONFIG_DIR, or the user's configuration directory.
func credentialsPath() (string, error) {
	dir := os.Getenv("LAUNCHWAY_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "launchway")
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// saveCredentials writes the file readable by its owner only, replacing it
// whole, so it is never seen half written or with wider permissions.
func saveCredentials(c credentials) (string, error) {
	path, err := credentialsPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

// loadCredentials returns the server and token to use: the environment's,
// else the stored ones.
func loadCredentials() (credentials, error) {
	var c credentials
	if path, err := credentialsPath(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &c); err != nil {
				return c, fmt.Errorf("%s is not readable: %v", path, err)
			}
			if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
				fmt.Fprintf(os.Stderr, "launchway: warning: %s can be read by others; it holds a token (chmod 600 it)\n", path)
			}
		}
	}
	if v := os.Getenv("LAUNCHWAY_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("LAUNCHWAY_TOKEN"); v != "" {
		c.Token = v
	}
	if c.Server == "" || c.Token == "" {
		return c, usageErr("not signed in: run launchway login --server URL, or set LAUNCHWAY_SERVER and LAUNCHWAY_TOKEN")
	}
	c.Server = strings.TrimRight(c.Server, "/")
	return c, nil
}

// cliErr is an error with the exit code it ends the command with.
type cliErr struct {
	code int
	msg  string
}

func (e cliErr) Error() string { return e.msg }

func usageErr(format string, a ...any) error { return cliErr{exitUsage, fmt.Sprintf(format, a...)} }
func apiErr(format string, a ...any) error   { return cliErr{exitAPI, fmt.Sprintf(format, a...)} }

// runClient runs a client command and turns its error into an exit code.
func runClient(fn func() error) error {
	err := fn()
	var ce cliErr
	if errors.As(err, &ce) {
		if ce.msg != "" {
			fmt.Fprintln(os.Stderr, "launchway:", ce.msg)
		}
		return exitErr(ce.code)
	}
	return err
}

// ---- talking to the server ----

type client struct {
	c     credentials
	http  *http.Client
	out   io.Writer
	words *settings.Words
}

// themed fills the words of the default set into fixed text such as the help, which is printed without
// asking a server: {run}, {runner}, {rollback:do} (see internal/settings/vocabulary.go).
func themed(s string) string { return settings.DefaultWords().Say(s) }

// say fills the words in a sentence for the person reading: the set and the renames of the installation
// this client talks to, read once from its configuration overview (the default words when it cannot be read).
// Flags, commands and JSON fields never change with a set.
func (cl *client) say(s string) string {
	if cl.words == nil {
		w := settings.DefaultWords()
		var v struct {
			Terms settings.Terms `json:"terms"`
		}
		if cl.call("GET", "/api/v1/config", nil, &v) == nil {
			w = v.Terms.Words()
		}
		cl.words = &w
	}
	return cl.words.Say(s)
}

func newClient(c credentials, out io.Writer) *client {
	return &client{c: c, http: &http.Client{Timeout: 30 * time.Second}, out: out}
}

// call sends a request and decodes the answer into v. Problems the server
// reports are returned as they are, including input values that do not
// fit, by name.
// rawText is a request body sent as it is, not as JSON: a pipeline file.
type rawText string

func (cl *client) call(method, path string, body, v any) error {
	var rd io.Reader
	if raw, ok := body.(rawText); ok {
		rd = strings.NewReader(string(raw))
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	// The address is the server the person configured for this command line, and the path is one of
	// the command's own; calling it is the point of the client.
	req, err := http.NewRequest(method, cl.c.Server+path, rd) //nolint:gosec // G704: the user's own configured server
	if err != nil {
		return usageErr("%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	req.Header.Set("User-Agent", "launchway-cli/"+version)
	// Not a browser: a server without sign-in (auth.mode: none) treats
	// every caller as its user and asks every change for this header,
	// which a page of another site cannot send.
	req.Header.Set("X-Launchway-CSRF", "1")
	if _, raw := body.(rawText); raw {
		req.Header.Set("Content-Type", "text/plain")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.http.Do(req) //nolint:gosec // G704: the CLI calls the Launchway server the user configured; that address is the point, not a tainted input
	if err != nil {
		return apiErr("cannot reach %s: %v", cl.c.Server, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error    string            `json:"error"`
			Next     string            `json:"next"`
			Fields   map[string]string `json:"fields"`
			Problems []struct {
				Line    int    `json:"line"`
				Path    string `json:"path"`
				Message string `json:"message"`
			} `json:"problems"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		switch {
		case len(e.Fields) > 0:
			names := make([]string, 0, len(e.Fields))
			for n := range e.Fields {
				names = append(names, n)
			}
			sort.Strings(names)
			msg = "the Sortie's inputs have problems:"
			for _, n := range names {
				msg += fmt.Sprintf("\n  %s: %s", n, e.Fields[n])
			}
		case len(e.Problems) > 0:
			for _, p := range e.Problems {
				msg += "\n  "
				if p.Line > 0 {
					msg += fmt.Sprintf("line %d: ", p.Line)
				}
				if p.Path != "" {
					msg += p.Path + ": "
				}
				msg += p.Message
			}
		case resp.StatusCode == http.StatusUnauthorized:
			msg += " (the token may have expired or been revoked: make a new one and run launchway login again)"
		}
		// The server says what to do about it (docs/development/interface-quality.md): say it on its own line.
		if e.Next != "" && resp.StatusCode != http.StatusUnauthorized {
			msg += "\nWhat to do: " + e.Next
		}
		return apiErr("%s", msg)
	}
	if raw, ok := v.(*[]byte); ok {
		*raw = data // a file, not JSON (see debrief export)
		return nil
	}
	if v != nil && len(data) > 0 {
		if err := json.Unmarshal(data, v); err != nil {
			return apiErr("the server's answer is not what this command expects: %v", err)
		}
	}
	return nil
}

// ---- login ----

func cmdLogin(args []string) error {
	return runClient(func() error {
		fs := flag.NewFlagSet("login", flag.ContinueOnError)
		server := fs.String("server", os.Getenv("LAUNCHWAY_SERVER"), "the server's URL")
		allowHTTP := fs.Bool("allow-http", false, "allow a server reached over plain HTTP that is not on this machine")
		if err := fs.Parse(args); err != nil {
			return cliErr{exitUsage, ""}
		}
		u, err := url.Parse(*server)
		if *server == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return usageErr("give the server's address: launchway login --server https://launchway.example.com")
		}
		if u.Scheme == "http" && !*allowHTTP && !isLoopbackHost(u.Hostname()) {
			return usageErr("%s is plain HTTP: the token would cross the network unencrypted. Use https, or --allow-http if you are sure", *server)
		}
		tok := strings.TrimSpace(os.Getenv("LAUNCHWAY_TOKEN"))
		if tok == "" {
			if isTerminal(os.Stdin) {
				fmt.Fprint(os.Stderr, "API token (make one in the web UI under API tokens): ")
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return err
				}
				tok = strings.TrimSpace(string(b))
			} else {
				line, err := bufio.NewReader(io.LimitReader(os.Stdin, 4096)).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				tok = strings.TrimSpace(line)
			}
		}
		if !strings.HasPrefix(tok, "lw_") {
			return usageErr("that is not a Launchway API token (they start with lw_)")
		}
		c := credentials{Server: strings.TrimRight(*server, "/"), Token: tok}
		var me struct {
			User string `json:"user"`
			Via  string `json:"via"`
		}
		if err := newClient(c, os.Stdout).call("GET", "/api/v1/me", nil, &me); err != nil {
			return err
		}
		path, err := saveCredentials(c)
		if err != nil {
			return err
		}
		fmt.Printf("Signed in to %s as %s. The token is kept in %s.\n", c.Server, me.User, path)
		return nil
	})
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ---- runs ----

func cmdRuns(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, clientUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "start":
			return cl.start(args[1:])
		case "list":
			return cl.list(args[1:])
		case "show":
			return cl.show(args[1:])
		case "logs":
			return cl.logs(args[1:])
		case "tests":
			return cl.tests(args[1:])
		case "coverage":
			return cl.coverage(args[1:])
		case "reports":
			return cl.reports(args[1:])
		case "cancel":
			return cl.cancel(args[1:])
		case "lanes":
			return cl.lanes(args[1:])
		case "queue":
			return cl.queue(args[1:])
		case "trust":
			return cl.trust(args[1:])
		case "resume":
			return cl.resume(args[1:])
		case "rerun":
			return cl.rerun(args[1:])
		}
		return usageErr("unknown command runs %s\n\n%s", args[0], clientUsage)
	})
}

// multi is a flag that may be given several times.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// parseArgs parses flags that may come before or after the positional
// arguments, and returns the positional ones.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, cliErr{exitUsage, ""}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (cl *client) start(args []string) error {
	fs := flag.NewFlagSet("runs start", flag.ContinueOnError)
	ref := fs.String("ref", "", "the branch or tag to run; default the project's default branch")
	var inputs multi
	fs.Var(&inputs, "i", "an input value, name=value; give it once for each input (a list: name=a,b,c)")
	wait := fs.Bool("wait", false, "wait for the run to finish, and exit with its outcome")
	timeout := fs.Duration("timeout", 0, "with --wait, stop waiting after this long (the run goes on)")
	asJSON := fs.Bool("json", false, "print the run as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which project: launchway runs start <project> [--ref R] [-i name=value ...] [--wait]")
	}
	vals := map[string]string{}
	for _, kv := range inputs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return usageErr("-i takes name=value, not %q", kv)
		}
		if _, dup := vals[k]; dup {
			return usageErr("the input %s is given twice", k)
		}
		vals[k] = v
	}
	body := map[string]any{"ref": *ref}
	if len(vals) > 0 {
		body["inputs"] = vals
	}
	var run runView
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/runs", body, &run); err != nil {
		return err
	}
	if !*wait {
		return cl.print(run, *asJSON, func() {
			fmt.Fprintf(cl.out, "Started run #%d of %s: %s/runs/%s\n", run.Number, run.Project, cl.c.Server, run.ID)
		})
	}
	if !*asJSON {
		fmt.Fprintf(os.Stderr, "Started run #%d of %s: %s/runs/%s\n", run.Number, run.Project, cl.c.Server, run.ID)
	}
	final, err := cl.waitFor(run.ID, *timeout, !*asJSON)
	if err != nil {
		return err
	}
	if err := cl.print(final, *asJSON, func() {
		fmt.Fprintf(cl.out, "Run #%d %s\n", final.Number, final.Status)
	}); err != nil {
		return err
	}
	return outcome(final.Status)
}

// outcome is the exit of a finished run.
func outcome(status string) error {
	switch status {
	case "succeeded":
		return nil
	case "canceled":
		return cliErr{exitRunCanceled, ""}
	}
	return cliErr{exitRunFailed, ""}
}

// pollEvery is how often waiting commands ask the server.
var pollEvery = 2 * time.Second

// waitFor waits for a run to finish, printing its steps as they change.
func (cl *client) waitFor(id string, timeout time.Duration, progress bool) (runView, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	seen := map[string]string{}
	for {
		var r runView
		if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(id), nil, &r); err != nil {
			return r, err
		}
		if progress {
			for _, s := range r.StepList {
				if seen[s.ID] != s.Status && s.Status != "pending" {
					fmt.Fprintf(os.Stderr, "  %-10s %s\n", s.Status, s.ID)
					seen[s.ID] = s.Status
				}
			}
		}
		if terminal(r.Status) {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, cliErr{exitTimeout, fmt.Sprintf("run #%d is still %s after %s; it goes on", r.Number, r.Status, timeout)}
		case <-time.After(pollEvery):
		}
	}
}

func (cl *client) print(v any, asJSON bool, text func()) error {
	if asJSON {
		enc := json.NewEncoder(cl.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text()
	return nil
}

func (cl *client) list(args []string) error {
	fs := flag.NewFlagSet("runs list", flag.ContinueOnError)
	project := fs.String("project", "", "only this project's runs")
	status := fs.String("status", "", "only runs with this status")
	lane := fs.String("lane", "", "only runs in the lane of this name")
	limit := fs.Int("limit", 20, "at most this many")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q := url.Values{}
	if *project != "" {
		q.Set("project", *project)
	}
	if *status != "" {
		q.Set("status", *status)
	}
	if *lane != "" {
		q.Set("lane", *lane)
	}
	q.Set("limit", strconv.Itoa(*limit))
	var runs []runView
	if err := cl.call("GET", "/api/v1/runs?"+q.Encode(), nil, &runs); err != nil {
		return err
	}
	return cl.print(runs, *asJSON, func() {
		for _, r := range runs {
			ref := runRef(r)
			line := fmt.Sprintf("%-24s %-16s #%-5d %-10s %-12s %-14s %s", r.ID, r.Project, r.Number, r.Status, r.Event, ref,
				time.UnixMilli(r.CreatedAt).Format(time.DateTime))
			if note := allowedNote(r.AllowedFailures, r.AllowedFailureSteps); note != "" {
				line += "  (" + note + ")"
			}
			fmt.Fprintln(cl.out, line)
		}
	})
}

// showTriggerAndLane prints what started a run apart from its commit, and where
// it stands in its lane.
func (cl *client) showTriggerAndLane(r *runView) {
	if d := r.TriggerDetail; d != nil {
		if d.Schedule != "" {
			fmt.Fprintf(cl.out, "  schedule %s\n", d.Schedule)
		}
		if d.Webhook != "" {
			fmt.Fprintf(cl.out, "  webhook %s\n", d.Webhook)
		}
		names := make([]string, 0, len(d.Inputs))
		for n := range d.Inputs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(cl.out, "  input %s = %s\n", n, d.Inputs[n])
		}
	}
	if l := r.Lane; l != nil {
		switch {
		case l.State == "waiting" && l.Behind != nil:
			fmt.Fprintf(cl.out, "  lane %s: waiting, number %d in line, behind run #%d\n", l.Label, l.Position, l.Behind.Number)
		case l.Behind != nil:
			fmt.Fprintf(cl.out, "  lane %s: %s (run #%d)\n", l.Label, l.State, l.Behind.Number)
		default:
			fmt.Fprintf(cl.out, "  lane %s: %s\n", l.Label, l.State)
		}
	}
}

func (cl *client) show(args []string) error {
	fs := flag.NewFlagSet("runs show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the whole run as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs show <run-id>")
	}
	if *asJSON {
		var raw json.RawMessage
		if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0]), nil, &raw); err != nil {
			return err
		}
		var buf bytes.Buffer
		_ = json.Indent(&buf, raw, "", "  ")
		fmt.Fprintln(cl.out, buf.String())
		return nil
	}
	var r runView
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0]), nil, &r); err != nil {
		return err
	}
	ref := runRef(r)
	fmt.Fprintf(cl.out, "Run #%d of %s: %s\n", r.Number, r.Project, r.Status)
	if note := allowedNote(r.AllowedFailures, r.AllowedFailureSteps); note != "" {
		fmt.Fprintf(cl.out, "  %s\n", note)
	}
	fmt.Fprintf(cl.out, "  %s at %.10s, %s by %s, %s\n", ref, r.SHA, r.Event, r.Actor, time.UnixMilli(r.CreatedAt).Format(time.DateTime))
	cl.showTriggerAndLane(&r)
	if r.CancelReason != "" {
		fmt.Fprintf(cl.out, "  canceled: %s\n", r.CancelReason)
	}
	if r.DeadlineAt > 0 && !terminal(r.Status) {
		fmt.Fprintf(cl.out, "  times out at %s\n", time.UnixMilli(r.DeadlineAt).Format(time.DateTime))
	}
	fmt.Fprintf(cl.out, "  %s/runs/%s\n\n", cl.c.Server, r.ID)
	printRunStages(cl.out, &r)
	if line := r.Estimate.Sentence(); line != "" {
		fmt.Fprintf(cl.out, "%s\n\n", line)
	}
	fmt.Fprintln(cl.out, "Steps:")
	defer printActions(cl.out, r.Actions)
	for _, s := range r.StepList {
		status := s.Status
		if s.AllowedFailure {
			status = "failed (allowed)"
		}
		line := fmt.Sprintf("  %-10s %s", status, s.ID)
		if s.Status == "queued" && s.NotBefore > time.Now().UnixMilli() {
			line += fmt.Sprintf(": retrying in %s (attempt %d of %d)", time.Until(time.UnixMilli(s.NotBefore)).Round(time.Second), s.Attempt, s.Retry+1)
		}
		if s.FailureCause != "" && s.FailureCause != "exit" {
			line += " [cause: " + s.FailureCause + "]"
		}
		if s.Workspace != "" {
			line += " [own workspace " + s.Workspace
			if s.Runner != "" {
				line += " on " + s.Runner
			}
			line += "]"
		}
		if s.WaitingFor != "" {
			line += " (" + s.WaitingFor + ")"
		}
		if s.Reason != "" {
			line += ": " + s.Reason
		} else if s.Error != "" {
			line += ": " + s.Error
		}
		line += usualNote(s.Estimate, r.Status, s.Status)
		fmt.Fprintln(cl.out, line)
		if s.Cause != nil {
			for _, e := range s.Cause.Evidence() {
				fmt.Fprintf(cl.out, "             %s\n", e)
			}
			if n := s.Cause.Next; n != nil {
				fmt.Fprintf(cl.out, "             then: %s\n", n.Summary)
			}
		}
		for _, c := range s.Caches {
			cl.printCache(c)
		}
	}
	return nil
}

func (cl *client) cancel(args []string) error {
	if len(args) != 1 {
		return usageErr("say which run: launchway runs cancel <run-id>")
	}
	if err := cl.call("POST", "/api/v1/runs/"+url.PathEscape(args[0])+"/cancel", map[string]any{}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Asked run %s to cancel.\n", args[0])
	return nil
}

type logLine struct {
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// logs prints a run's logs, step by step in the pipeline's order; with
// --follow it keeps printing new lines until the run finishes.
func (cl *client) logs(args []string) error {
	fs := flag.NewFlagSet("runs logs", flag.ContinueOnError)
	step := fs.String("step", "", "only this step")
	follow := fs.Bool("follow", false, "keep printing new lines until the Sortie finishes")
	service := fs.String("service", "", "print this service container's output instead of the steps'")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 2 && *step == "" {
		*step = pos[1] // launchway runs logs --follow <run> <step>
		pos = pos[:1]
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs logs <run-id> [step] [--step S] [--service NAME] [--follow]")
	}
	id := pos[0]
	type progress struct {
		attempt, after int
		header         bool
	}
	read := map[string]*progress{}
	for {
		var r runView
		if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(id), nil, &r); err != nil {
			return err
		}
		found := *step == ""
		for _, s := range r.StepList {
			if *step != "" && s.ID != *step {
				continue
			}
			found = true
			if s.Status == "pending" || s.Status == "queued" || s.Attempt == 0 {
				continue
			}
			p := read[s.ID]
			if p == nil || p.attempt != s.Attempt {
				p = &progress{attempt: s.Attempt}
				read[s.ID] = p
			}
			for {
				var page struct {
					Attempt int       `json:"attempt"`
					Lines   []logLine `json:"lines"`
				}
				path := fmt.Sprintf("/api/v1/runs/%s/steps/%s/logs?attempt=%d&after=%d&limit=1000", url.PathEscape(id), url.PathEscape(s.ID), p.attempt, p.after) + serviceQuery(*service)
				if err := cl.call("GET", path, nil, &page); err != nil {
					return err
				}
				if len(page.Lines) == 0 {
					break
				}
				if !p.header && *step == "" {
					fmt.Fprintf(cl.out, "── %s (attempt %d)\n", s.ID, p.attempt)
					p.header = true
				}
				for _, l := range page.Lines {
					fmt.Fprintln(cl.out, l.Text)
					p.after = l.Seq
				}
			}
		}
		if !found {
			return apiErr("run %s has no step %s", id, *step)
		}
		if !*follow || terminal(r.Status) {
			return nil
		}
		// Lines reach the server within about a second of being printed;
		// following asks twice as often as the other waits do.
		time.Sleep(min(pollEvery, followEvery))
	}
}

// followEvery is how often --follow asks for new lines.
const followEvery = 400 * time.Millisecond

// bindLocal binds a pipeline run on this machine to its input values
// (given as -i name=value), as a server does.
func bindLocal(p *pipeline.Pipeline, given []string) (*pipeline.Pipeline, map[string]string, error) {
	vals := map[string]string{}
	for _, kv := range given {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("-i takes name=value, not %q", kv)
		}
		vals[k] = v
	}
	resolved, err := pipeline.ResolveInputs(p, vals)
	if err != nil {
		return nil, nil, err
	}
	b, err := pipeline.Bind(p, resolved, pipeline.BindOptions{})
	if err != nil {
		return nil, nil, err
	}
	if len(resolved) == 0 {
		resolved = nil
	}
	return b, resolved, nil
}

// lanes lists a project's lanes that are held or have runs waiting.
func (cl *client) lanes(args []string) error {
	fs := flag.NewFlagSet("runs lanes", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which project: launchway runs lanes <project> [--json]")
	}
	type ref struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Status string `json:"status"`
	}
	var lanes []struct {
		Label   string `json:"label"`
		Holder  *ref   `json:"holder"`
		Waiting []ref  `json:"waiting"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/lanes", nil, &lanes); err != nil {
		return err
	}
	return cl.print(lanes, *asJSON, func() {
		if len(lanes) == 0 {
			fmt.Fprintln(cl.out, "No lane is held and no run is waiting for one.")
		}
		for _, l := range lanes {
			holder := "no Sortie holds it"
			if l.Holder != nil {
				holder = fmt.Sprintf("held by run #%d (%s)", l.Holder.Number, l.Holder.Status)
			}
			fmt.Fprintf(cl.out, "%s: %s\n", l.Label, holder)
			for i, w := range l.Waiting {
				fmt.Fprintf(cl.out, "  %d. run #%d waits\n", i+1, w.Number)
			}
		}
	})
}

// resume continues a failed or canceled run: the same run, its failed and
// canceled steps run again, what succeeded is kept. With --wait it exits as
// runs start --wait does.
func (cl *client) resume(args []string) error {
	fs := flag.NewFlagSet("runs resume", flag.ContinueOnError)
	wait := fs.Bool("wait", false, "wait for the run to finish, and exit with its outcome")
	timeout := fs.Duration("timeout", 0, "with --wait, stop waiting after this long (the run goes on)")
	asJSON := fs.Bool("json", false, "print the run as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs resume <run-id> [--wait]")
	}
	if err := cl.call("POST", "/api/v1/runs/"+url.PathEscape(pos[0])+"/resume", map[string]any{}, nil); err != nil {
		return err
	}
	return cl.followRun(pos[0], *wait, *timeout, *asJSON, "Resumed run %s: its failed steps run again.\n")
}

// followRun prints, or waits for, a run a command just made or resumed.
func (cl *client) followRun(id string, wait bool, timeout time.Duration, asJSON bool, started string) error {
	if !wait {
		var run runView
		if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(id), nil, &run); err != nil {
			return err
		}
		return cl.print(run, asJSON, func() {
			fmt.Fprintf(cl.out, started, id)
			fmt.Fprintf(cl.out, "Run #%d of %s: %s/runs/%s\n", run.Number, run.Project, cl.c.Server, run.ID)
		})
	}
	if !asJSON {
		fmt.Fprintf(os.Stderr, started, id)
	}
	final, err := cl.waitFor(id, timeout, !asJSON)
	if err != nil {
		return err
	}
	if err := cl.print(final, asJSON, func() {
		fmt.Fprintf(cl.out, "Run #%d %s\n", final.Number, final.Status)
	}); err != nil {
		return err
	}
	return outcome(final.Status)
}

// rerunPlan is what a re-run would do, as the server says.
type rerunPlan struct {
	From  []string `json:"from"`
	Run   []string `json:"run"`
	Reuse []string `json:"reuse"`
}

// rerun makes a new run of a finished one from steps, or from the ones that
// failed. With --plan it only says what would run and what would be reused,
// or why it cannot be done.
func (cl *client) rerun(args []string) error {
	fs := flag.NewFlagSet("runs rerun", flag.ContinueOnError)
	var froms multi
	fs.Var(&froms, "from", "a step to start from; give it once for each step")
	failed := fs.Bool("failed", false, "start from the smallest set of steps that covers every failed and canceled step")
	plan := fs.Bool("plan", false, "only say what would run again and what would be reused")
	wait := fs.Bool("wait", false, "wait for the new run to finish, and exit with its outcome")
	timeout := fs.Duration("timeout", 0, "with --wait, stop waiting after this long (the run goes on)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (len(froms) == 0 && !*failed) {
		return usageErr("launchway runs rerun <run-id> [--from STEP]... [--failed] [--plan] [--wait]: name the steps with --from, or use --failed")
	}
	id := pos[0]
	if *plan {
		q := url.Values{}
		for _, f := range froms {
			q.Add("from", f)
		}
		if *failed {
			q.Set("failed", "true")
		}
		var p rerunPlan
		if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(id)+"/rerun?"+q.Encode(), nil, &p); err != nil {
			return err
		}
		return cl.print(p, *asJSON, func() {
			fmt.Fprintf(cl.out, "Re-running from %s would run again:\n", strings.Join(p.From, ", "))
			for _, s := range p.Run {
				fmt.Fprintf(cl.out, "  run    %s\n", s)
			}
			for _, s := range p.Reuse {
				fmt.Fprintf(cl.out, "  reuse  %s\n", s)
			}
		})
	}
	body := map[string]any{"from": []string(froms), "failed": *failed}
	var run runView
	if err := cl.call("POST", "/api/v1/runs/"+url.PathEscape(id)+"/rerun", body, &run); err != nil {
		return err
	}
	if !*wait {
		return cl.print(run, *asJSON, func() {
			fmt.Fprintf(cl.out, "Started run #%d of %s, a re-run: %s/runs/%s\n", run.Number, run.Project, cl.c.Server, run.ID)
		})
	}
	return cl.followRun(run.ID, true, *timeout, *asJSON, "Started a re-run, run %s.\n")
}
