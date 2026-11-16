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

const clientUsage = `Usage:
  launchway login --server URL              store an API token for the server (read from
                  [--allow-http]            LAUNCHWAY_TOKEN, or typed or piped in)
  launchway runs start <project> [--ref R] [-i name=value ...] [--wait] [--timeout D] [--json]
  launchway runs list [--project P] [--status S] [--limit N] [--json]
  launchway runs show <run-id> [--json]
  launchway runs logs <run-id> [--step S] [--follow]
  launchway runs cancel <run-id>

LAUNCHWAY_SERVER and LAUNCHWAY_TOKEN, if set, are used instead of what login
stored. Exit codes: 0 success; 1 the run failed; 2 usage; 3 the run was
canceled; 4 the server refused or could not be reached; 5 --timeout passed.
`

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
	c    credentials
	http *http.Client
	out  io.Writer
}

func newClient(c credentials, out io.Writer) *client {
	return &client{c: c, http: &http.Client{Timeout: 30 * time.Second}, out: out}
}

// call sends a request and decodes the answer into v. Problems the server
// reports are returned as they are, including input values that do not
// fit, by name.
func (cl *client) call(method, path string, body, v any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cl.c.Server+path, rd)
	if err != nil {
		return usageErr("%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	req.Header.Set("User-Agent", "launchway-cli/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.http.Do(req)
	if err != nil {
		return apiErr("cannot reach %s: %v", cl.c.Server, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error    string            `json:"error"`
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
			msg = "the run's inputs have problems:"
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
		return apiErr("%s", msg)
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
		case "cancel":
			return cl.cancel(args[1:])
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

type runView struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Number     int    `json:"number"`
	Status     string `json:"status"`
	Event      string `json:"event"`
	Branch     string `json:"branch"`
	Tag        string `json:"tag"`
	SHA        string `json:"sha"`
	Actor      string `json:"actor"`
	Message    string `json:"message"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at"`
	StepList   []struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Attempt  int    `json:"attempt"`
		Error    string `json:"error"`
		ExitCode *int   `json:"exit_code"`
	} `json:"step_list"`
	TriggerDetail *struct {
		Inputs   map[string]string `json:"inputs"`
		Schedule string            `json:"schedule"`
		Webhook  string            `json:"webhook"`
	} `json:"trigger_detail"`
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "canceled"
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
	q.Set("limit", strconv.Itoa(*limit))
	var runs []runView
	if err := cl.call("GET", "/api/v1/runs?"+q.Encode(), nil, &runs); err != nil {
		return err
	}
	return cl.print(runs, *asJSON, func() {
		for _, r := range runs {
			ref := r.Branch
			if ref == "" {
				ref = r.Tag
			}
			fmt.Fprintf(cl.out, "%-24s %-16s #%-5d %-10s %-12s %-14s %s\n", r.ID, r.Project, r.Number, r.Status, r.Event, ref,
				time.UnixMilli(r.CreatedAt).Format(time.DateTime))
		}
	})
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
	ref := r.Branch
	if ref == "" {
		ref = r.Tag
	}
	fmt.Fprintf(cl.out, "Run #%d of %s: %s\n", r.Number, r.Project, r.Status)
	fmt.Fprintf(cl.out, "  %s at %.10s, %s by %s, %s\n", ref, r.SHA, r.Event, r.Actor, time.UnixMilli(r.CreatedAt).Format(time.DateTime))
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
	fmt.Fprintf(cl.out, "  %s/runs/%s\n\nSteps:\n", cl.c.Server, r.ID)
	for _, s := range r.StepList {
		line := fmt.Sprintf("  %-10s %s", s.Status, s.ID)
		if s.Error != "" {
			line += ": " + s.Error
		}
		fmt.Fprintln(cl.out, line)
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
	follow := fs.Bool("follow", false, "keep printing new lines until the run finishes")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs logs <run-id> [--step S] [--follow]")
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
				path := fmt.Sprintf("/api/v1/runs/%s/steps/%s/logs?attempt=%d&after=%d&limit=1000", url.PathEscape(id), url.PathEscape(s.ID), p.attempt, p.after)
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
		time.Sleep(pollEvery)
	}
}

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
