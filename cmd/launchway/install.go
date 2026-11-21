package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/jk-magic-creations/launchway/internal/installer"
	"github.com/jk-magic-creations/launchway/internal/installer/kube"
)

// imageRef is the container image of this build, set at release time with
// -ldflags "-X main.imageRef=ghcr.io/...@sha256:...". Empty on a development build.
var imageRef = ""

const installHelp = `launchway install: the guided installer.

  launchway install                       ask a few plain questions, show what it will do, then do it
  launchway install --answers FILE        answer from a file (no questions asked; see the install guide)
  launchway install --dry-run             show what would be done and change nothing
  launchway install --uninstall           remove what the installer made; says what it keeps (root key and data)
  launchway install bundle --output FILE  write an offline bundle (this program, the chart and the images)
  launchway install --bundle FILE         install from an offline bundle

Flags:
  --answers FILE      a YAML file of answers: key: value, one per question (every question prints its key in the transcript)
  --set KEY=VALUE     one answer (repeat); wins over the file
  --target T          this-machine or kubernetes (same as --set target=T)
  --dry-run           change nothing; print the summary
  --yes               do not ask the final "go ahead?" (for scripts)
  --non-interactive   never ask; every question is answered from the answers or its default
  --transcript FILE   where to write the transcript, which never holds a secret (default launchway-install.log)
  --no-browser        do not offer to open the browser
  Kubernetes: --kubeconfig FILE, --context NAME, --namespace NAME, --image REF

The installer asks where Launchway should run: on this machine by itself, or on a Kubernetes cluster you have.
It needs no helm and no kubectl. It never regenerates the root key, and it is safe to run again.
`

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdInstall(args []string) error {
	if len(args) > 0 && args[0] == "bundle" {
		return cmdInstallBundle(args[1:])
	}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var sets multiFlag
	answersFile := fs.String("answers", "", "")
	fs.Var(&sets, "set", "")
	target := fs.String("target", "", "")
	dry := fs.Bool("dry-run", false, "")
	un := fs.Bool("uninstall", false, "")
	yes := fs.Bool("yes", false, "")
	nonInt := fs.Bool("non-interactive", false, "")
	tr := fs.String("transcript", "launchway-install.log", "")
	noBrowser := fs.Bool("no-browser", false, "")
	bundle := fs.String("bundle", "", "")
	kubeconfig := fs.String("kubeconfig", "", "")
	kctx := fs.String("context", "", "")
	ns := fs.String("namespace", "", "")
	image := fs.String("image", "", "")
	if err := fs.Parse(args); err != nil {
		fmt.Print(installHelp)
		return usageErr("%v", err)
	}
	answers := map[string]string{}
	if *answersFile != "" {
		a, err := installer.LoadAnswers(*answersFile)
		if err != nil {
			return err
		}
		answers = a
	}
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return usageErr("--set wants KEY=VALUE, got %q", s)
		}
		answers[k] = v
	}
	if *target != "" {
		answers["target"] = *target
	}
	for k := range answers {
		if installer.IsSecretKey(k) {
			return usageErr("the answer %q looks like a secret: answers files are plain text and are kept. Give the name of a file or variable that holds it instead (a key ending in _file or _env)", k)
		}
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && !*nonInt
	env := installer.Default(os.Stdin, os.Stdout, version, imageRef)
	env.Interactive = interactive
	opts := installer.Options{Answers: answers, DryRun: *dry, Uninstall: *un, Assume: *yes || !interactive && *answersFile != "", TranscriptPath: *tr,
		NoBrowser: *noBrowser || !interactive, Bundle: *bundle, Doctor: installDoctor,
		Kube: &kube.Installer{Kubeconfig: *kubeconfig, Context: *kctx, Namespace: *ns, Image: *image}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := installer.Run(ctx, env, opts)
	if err == nil && res.Address != "" {
		fmt.Printf("\nThe transcript of this install (no secrets in it) is in %s\n", *tr)
	}
	return err
}

func init() { helpTexts["install"] = installHelp } // help needs no cluster and no server
