package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jk-magic-creations/launchway/internal/localauth"
)

// runLocal is the "this machine, by itself" target: a laptop trial or a server.
func runLocal(ctx context.Context, e *Env, a *Asker, o Options, t *Transcript, res Result) (Result, error) {
	mode, err := a.Ask(Question{Key: "local.mode", Prompt: "What is this machine for?", Default: "trial", Choices: []Choice{
		{Value: "trial", Label: "Trying it out or personal use on this machine (only this machine can reach it)"},
		{Value: "server", Label: "A server that other people will use (a service that starts at boot)"},
	}})
	if err != nil {
		return res, err
	}
	if mode == "server" {
		return runServerMode(ctx, e, a, o, t, res)
	}
	return runTrial(ctx, e, a, o, t, res)
}

// trialLayout is where a laptop trial keeps things: one directory per user.
type trialLayout struct{ base string }

func (l trialLayout) config() string { return filepath.Join(l.base, "launchway.yaml") }
func (l trialLayout) data() string   { return filepath.Join(l.base, "data") }
func (l trialLayout) run() string    { return filepath.Join(l.base, "run") }
func (l trialLayout) pid() string    { return filepath.Join(l.run(), "launchway.pid") }
func (l trialLayout) log() string    { return filepath.Join(l.run(), "server.log") }
func (l trialLayout) code() string   { return filepath.Join(l.run(), "setup-code") }

func defaultTrialDir(e *Env) string {
	if x := e.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "launchway")
	}
	if e.GOOS == "darwin" {
		return filepath.Join(e.Home, "Library", "Application Support", "launchway")
	}
	return filepath.Join(e.Home, ".local", "share", "launchway")
}

func (e *Env) freePort(from int) int {
	for p := from; p < from+100; p++ {
		if !e.Reachable("127.0.0.1:" + strconv.Itoa(p)) {
			return p
		}
	}
	return from
}

func runTrial(ctx context.Context, e *Env, a *Asker, o Options, t *Transcript, res Result) (Result, error) {
	dir, err := a.Ask(Question{Key: "local.dir", Prompt: "Where should Launchway keep its data on this machine?", Default: defaultTrialDir(e),
		Help: "One folder, only you can read it. It holds the database, the settings and the logs. Nothing else on this machine is touched."})
	if err != nil {
		return res, err
	}
	l := trialLayout{base: dir}
	res.ConfigPath = l.config()
	port := 0
	if cur, ok := readListenPort(l.config()); ok {
		port = cur // already installed: keep its port (re-running changes nothing)
	}
	if port == 0 {
		p, err := a.Ask(Question{Key: "local.port", Prompt: "Which port?", Default: strconv.Itoa(e.freePort(8080)),
			Help: "Launchway listens on 127.0.0.1 only, so no other machine can reach it."})
		if err != nil {
			return res, err
		}
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return res, fmt.Errorf("%q is not a port number", p)
		}
	}
	signin, err := a.Ask(Question{Key: "local.signin", Prompt: "How do you sign in?", Default: "accounts", Choices: []Choice{
		{Value: "accounts", Label: "With a built-in account: a one-time setup code makes the first administrator (recommended)"},
		{Value: "none", Label: "No sign-in, for this local trial only (anyone who can use this machine has full access)"},
	}})
	if err != nil {
		return res, err
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	url := "http://" + addr
	running := l.alive(e) || (e.Reachable(addr) && fileExists(l.config()))

	plan := Plan{Keeps: []string{"nothing else on this machine is changed: no service is installed, no system file is written, no network port is opened beyond 127.0.0.1"}}
	plan.Steps = append(plan.Steps, Step{Title: "Make the folder " + dir + " (readable by you only)", Shell: "mkdir -p -m 700 " + dir,
		Do: func(context.Context) error { return os.MkdirAll(l.run(), 0o700) }})
	plan.Steps = append(plan.Steps, Step{Title: "Write the settings " + l.config() + " (kept as it is if it already exists)", Do: func(context.Context) error {
		_, err := writeIfAbsent(l.config(), []byte(trialConfig(l, addr, signin)), 0o600)
		return err
	}})
	var code string
	if signin == "accounts" {
		plan.Steps = append(plan.Steps, Step{Title: "Make a one-time setup code for the first administrator (kept if one exists)", Do: func(context.Context) error {
			c := localauth.NewSetupCode()
			wrote, err := writeIfAbsent(l.code(), []byte(c+"\n"), 0o600)
			if err != nil {
				return err
			}
			if wrote {
				code = c
			}
			return nil
		}})
	}
	if running {
		plan.Steps = append(plan.Steps, Step{Title: "Launchway is already running on " + addr + ": leave it running", Do: func(context.Context) error { return nil }})
	} else {
		plan.Steps = append(plan.Steps, Step{Title: "Start Launchway in the background on " + addr, Shell: e.Binary + " server --config " + l.config(), Do: func(ctx context.Context) error {
			pid, err := e.Start(e.Binary, []string{"server", "--config", l.config()}, nil, l.base, l.log())
			if err != nil {
				return fmt.Errorf("could not start the server: %w", err)
			}
			_ = os.WriteFile(l.pid(), []byte(strconv.Itoa(pid)), 0o600)
			return waitReachable(ctx, e, addr, 60*time.Second, l.log())
		}})
	}
	plan.Summarize(e, "Here is what I am about to do:")
	res.Steps = len(plan.Steps)
	ok, err := goAhead(a, o, o.DryRun)
	if err != nil || !ok {
		if o.DryRun {
			e.Printf("\nDry run: nothing was changed.\n")
		}
		return res, err
	}
	if err := plan.Execute(ctx, e, t); err != nil {
		return res, err
	}
	res.Address = url
	if code == "" && signin == "accounts" {
		if b, err := os.ReadFile(l.code()); err == nil {
			code = strings.TrimSpace(string(b))
		}
	}
	t.Hide(code)
	e.Printf("\nLaunchway is running at %s\n", url)
	if signin == "accounts" && !setupRequired(ctx, url) {
		e.Printf("The first administrator has already been made: open it and sign in.\n")
	} else if signin == "accounts" {
		e.Printf("Open it and enter this one-time setup code to make the first administrator (valid 24 hours, works once):\n\n    %s\n\n", code)
		e.Printf("Lost it or expired? Run:  %s users setup-code --config %s\n", e.Binary, l.config())
	} else {
		e.Printf("There is NO sign-in on this local trial: anyone who can use this machine has full access. It listens on 127.0.0.1 only.\n")
	}
	e.Printf("To stop it and remove what the installer made (your data is kept):  %s install --uninstall\n", e.Binary)
	e.Printf("To start it again after a restart of this machine:  %s server --config %s\n", e.Binary, l.config())
	finish(ctx, e, a, o, t, url, DoctorInput{URL: url, ConfigPath: l.config(), Facts: map[string]string{"loopback": "yes", "sign_in": signin, "tls": "loopback"}})
	return res, nil
}

func trialConfig(l trialLayout, addr, signin string) string {
	var b strings.Builder
	b.WriteString("# Written by `launchway install` (laptop trial). Safe to edit; the installer keeps this file as it is.\n")
	fmt.Fprintf(&b, "listen: %s\ndata: %s\n", addr, l.data())
	if signin == "none" {
		b.WriteString("auth:\n  mode: none   # no sign-in: allowed only because the server listens on this machine alone\n")
	} else {
		fmt.Fprintf(&b, "auth:\n  mode: local\n  local:\n    setup_code_file: %s\n", l.code())
	}
	return b.String()
}

func readListenPort(cfg string) (int, bool) {
	b, err := os.ReadFile(cfg)
	if err != nil {
		return 0, false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(ln, "listen:"); ok {
			v = strings.TrimSpace(v)
			if i := strings.LastIndexByte(v, ':'); i >= 0 {
				if p, err := strconv.Atoi(v[i+1:]); err == nil {
					return p, true
				}
			}
		}
	}
	return 0, false
}

func (l trialLayout) alive(e *Env) bool {
	pid := readPID(l.pid())
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func waitReachable(ctx context.Context, e *Env, addr string, d time.Duration, logFile string) error {
	deadline := e.Now().Add(d)
	for e.Now().Before(deadline) {
		if e.Reachable(addr) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("it did not answer on %s within %s; the last lines of its log %s say why", addr, d, logFile)
}

// finish ends every target the same way: the checklist, the browser.
func finish(ctx context.Context, e *Env, a *Asker, o Options, t *Transcript, url string, in DoctorInput) {
	if o.Doctor != nil {
		e.Printf("\nA plain security check of what you just installed:\n")
		o.Doctor(ctx, e.Out, in)
	}
	if o.NoBrowser || e.OpenBrowser == nil {
		return
	}
	if yes, _ := a.Confirm("open_browser", "Open "+url+" in your browser now?", e.Interactive); yes {
		if err := e.OpenBrowser(url); err != nil {
			e.Printf("Could not open a browser from here (%v). Open %s yourself.\n", err, url)
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// setupRequired asks the server whether the first administrator is still to be made. If it cannot
// tell, it says yes: showing a code that is not needed is better than hiding one that is.
func setupRequired(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/setup", nil)
	if err != nil {
		return true
	}
	r, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return true
	}
	defer r.Body.Close()
	var v struct {
		Required *bool `json:"required"`
	}
	if json.NewDecoder(r.Body).Decode(&v) != nil || v.Required == nil {
		return true
	}
	return *v.Required
}
