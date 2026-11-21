package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Options are what the command line gave.
type Options struct {
	Answers   map[string]string
	DryRun    bool
	Uninstall bool
	// Assume says "yes" to the final "go ahead?" question without asking (unattended).
	Assume bool
	// TranscriptPath: where the transcript goes (without secrets). Empty: not written.
	TranscriptPath string
	// TranscriptTo is used instead of TranscriptPath when set (tests).
	TranscriptTo io.Writer
	NoBrowser    bool
	// Bundle is an offline bundle file to install from (see bundle.go).
	Bundle string
	// Kube is the Kubernetes side, wired by the command (it needs the cluster libraries).
	Kube KubeInstaller
	// Doctor prints the security checklist; wired by the command.
	Doctor func(ctx context.Context, w io.Writer, in DoctorInput)
}

// DoctorInput is what the closing checklist is built from.
type DoctorInput struct {
	URL        string
	ConfigPath string
	Kubernetes bool
	Facts      map[string]string // "sign_in", "tls", "network_policies", "loopback" ...
}

// Result is what the run did, for the transcript, the tests and the measured claims.
type Result struct {
	Target     string
	Address    string
	Typed      int // answers the person typed
	Asked      int // questions asked
	Unused     []string
	Steps      int
	Dry        bool
	Elapsed    time.Duration
	ConfigPath string
}

// KubeInstaller is the Kubernetes target. It is an interface so this package does not depend on the
// cluster libraries, and so the flow can be tested with a fake.
type KubeInstaller interface {
	Run(ctx context.Context, e *Env, a *Asker, o Options, t *Transcript) (Result, error)
}

// Run is `launchway install`.
func Run(ctx context.Context, env Env, o Options) (res Result, err error) {
	start := env.Now()
	tw := o.TranscriptTo
	var closer io.Closer
	if tw == nil && o.TranscriptPath != "" {
		f, ferr := createPrivate(o.TranscriptPath)
		if ferr != nil {
			return res, fmt.Errorf("the transcript file: %w", ferr)
		}
		tw, closer = f, f
		defer closer.Close()
	}
	t := NewTranscript(tw, env.Now)
	env.Out = t.Tee(env.Out)
	t.Line("launchway install %s, %s", env.Version, env.GOOS)
	a := NewAsker(&env, o.Answers, t)
	defer func() {
		res.Typed, res.Asked, res.Unused = a.Typed, a.Asked, a.Unused()
		res.Elapsed = env.Now().Sub(start)
		if len(res.Unused) > 0 {
			env.Printf("\nThese answers were given but nothing asked for them (a typo?): %s\n", strings.Join(res.Unused, ", "))
		}
		t.Line("questions asked: %d, answers typed: %d, took %s", a.Asked, a.Typed, res.Elapsed.Round(time.Second))
		if err != nil {
			t.Line("stopped: %v", err)
		}
	}()

	env.Printf("Launchway installer. Plain questions; press Enter to accept the answer in brackets. Nothing changes until you have seen a summary and said yes.\n")
	if o.DryRun {
		env.Printf("DRY RUN: nothing will be changed. You will see what would be done.\n")
	}
	target, err := a.Ask(Question{Key: "target", Prompt: "Where should Launchway run?", Default: "this-machine", Choices: []Choice{
		{Value: "this-machine", Label: "On this machine, by itself (a laptop trial, or one server)"},
		{Value: "kubernetes", Label: "On a Kubernetes cluster I already have"},
		{Value: "small-kubernetes", Label: "Set up a small Kubernetes on this machine for me", Unavailable: SmallKubernetesWhy},
	}})
	if err != nil {
		return res, err
	}
	res.Target, res.Dry = target, o.DryRun
	switch target {
	case "this-machine":
		if o.Uninstall {
			return runUninstall(ctx, &env, a, o, t, res)
		}
		return runLocal(ctx, &env, a, o, t, res)
	case "kubernetes":
		if o.Kube == nil {
			return res, errors.New("this build cannot talk to Kubernetes")
		}
		r, err := o.Kube.Run(ctx, &env, a, o, t)
		r.Target = target
		return r, err
	}
	return res, fmt.Errorf("unknown target %q", target)
}

// SmallKubernetesWhy is shown beside the menu entry that is not built yet.
const SmallKubernetesWhy = "not built yet: it needs root and changes this machine, and has not been tested here. See the install guide for the design (k3s). Until then, choose a cluster you have, or run Launchway by itself."

// Step is one thing the installer will do, shown in the summary before any change.
type Step struct {
	Title string
	// Shell is the equivalent command, for the person who would rather run it themselves
	// (and for the case where the installer lacks the rights).
	Shell string
	Do    func(ctx context.Context) error
}

// Plan is the steps, and what stays.
type Plan struct {
	Steps []Step
	Keeps []string
}

// Summarize prints "here is what I am about to do".
func (p Plan) Summarize(e *Env, heading string) {
	e.Printf("\n%s\n", heading)
	for i, s := range p.Steps {
		e.Printf("  %d. %s\n", i+1, s.Title)
	}
	if len(p.Keeps) > 0 {
		e.Printf("What stays as it is:\n")
		for _, k := range p.Keeps {
			e.Printf("  - %s\n", k)
		}
	}
}

// Execute runs the steps in order, stopping at the first failure. A step that is safe to repeat
// makes re-running the installer finish what an interruption left.
func (p Plan) Execute(ctx context.Context, e *Env, t *Transcript) error {
	for i, s := range p.Steps {
		e.Printf("[%d/%d] %s ...\n", i+1, len(p.Steps), s.Title)
		if err := s.Do(ctx); err != nil {
			e.Printf("      did not finish: %v\n", err)
			if s.Shell != "" {
				e.Printf("      The same step by hand:  %s\n", s.Shell)
			}
			e.Printf("      Nothing was undone. Run the installer again after fixing this: it carries on from where it stopped and never makes a second root key.\n")
			return fmt.Errorf("step %d (%s): %w", i+1, s.Title, err)
		}
	}
	return nil
}

// goAhead asks the closing confirmation.
func goAhead(a *Asker, o Options, dry bool) (bool, error) {
	if dry {
		return false, nil
	}
	if o.Assume {
		return true, nil
	}
	return a.Confirm("proceed", "Go ahead with this?", true)
}
