package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/store"
)

const basesHelp = `launchway bases: the Bases (customers) of this installation, from the server's own console.

  launchway bases list                          the Bases, their labels and state
  launchway bases create LABEL [--name TEXT] [--host NAME]
                                                make a Base (needs multi_base: true)
  launchway bases suspend LABEL                 turn a Base off without removing anything
  launchway bases resume LABEL                  turn it on again

Every installation has one Base, called default, and needs nothing here. A second Base needs
"multi_base: true" in the bootstrap file, which rewrites the identity tables' keys once and
cannot be undone except from a backup (docs/development/the-wall.md). This release only proves
the separation: the server refuses to start with multi_base on, until requests know their Base.
Options: --config FILE (default launchway.yaml).
`

func cmdBases(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(basesHelp)
		return nil
	}
	fs := flag.NewFlagSet("bases", flag.ContinueOnError)
	cfgPath := fs.String("config", "launchway.yaml", "the server's bootstrap file")
	name := fs.String("name", "", "display name of a new Base")
	host := fs.String("host", "", "host name of a new Base")
	// Flags may come after the words; parse in two passes.
	words, flags := splitFlags(args[1:])
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageErr("%v", err)
	}
	cfg, err := config.LoadBootstrap(*cfgPath)
	if err != nil {
		return err
	}
	wait, _, err := cfg.Timings()
	if err != nil {
		return err
	}
	eng, err := engine.Open(engine.Options{Database: cfg.DatabaseAddr(), ConnectWait: wait, MultiBase: cfg.MultiBase,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return err
	}
	defer eng.Close()
	return runBases(eng, args[0], words, *name, *host, os.Stdout, time.Now())
}

// splitFlags separates the --flag arguments from the plain words, so that
// `bases create acme --name X` and `bases create --name X acme` both work.
func splitFlags(args []string) (words, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && a != "-h" && a != "--help" {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		words = append(words, a)
	}
	return words, flags
}

// runBases is the commands of `launchway bases`, on an open engine.
func runBases(eng *engine.Engine, sub string, words []string, name, host string, out io.Writer, now time.Time) error {
	ctx := eng.BackgroundContext()
	reg := eng.Bases()
	who := "console"
	if u := os.Getenv("USER"); u != "" {
		who += ":" + u
	}
	audit := func(action, detail string) {
		_ = eng.Audit(engine.AuditEntry{At: now.UnixMilli(), User: who, Source: "console", Action: action, Outcome: "ok", Detail: detail})
	}
	switch sub {
	case "list":
		bases, err := reg.List(ctx)
		if err != nil {
			return err
		}
		for _, b := range bases {
			fmt.Fprintf(out, "%-24s %-10s %s\n", b.Handle, b.Status, b.Host)
		}
		if !reg.Multi() {
			fmt.Fprintln(out, "(One Base: several Bases are off. See `launchway bases --help`.)")
		}
		return nil
	case "create":
		if len(words) != 1 {
			return usageErr("launchway bases create LABEL [--name TEXT] [--host NAME]")
		}
		b, err := reg.CreateBase(ctx, store.NewBase{Handle: words[0], Name: name, Host: host, CreatedBy: who, Now: now.UnixMilli()})
		if errors.Is(err, store.ErrDisabled) {
			return fmt.Errorf("%w: set multi_base: true in the bootstrap file first (docs/development/the-wall.md says what that changes)", err)
		}
		if err != nil {
			return err
		}
		audit("base.create", b.Handle)
		fmt.Fprintf(out, "Base %s created.\n", b.Handle)
		return nil
	case "suspend", "resume":
		if len(words) != 1 {
			return usageErr("launchway bases %s LABEL", sub)
		}
		bases, err := reg.List(ctx)
		if err != nil {
			return err
		}
		for _, b := range bases {
			if b.Handle != strings.ToLower(words[0]) {
				continue
			}
			status := "suspended"
			if sub == "resume" {
				status = "active"
			}
			if err := reg.SetStatus(ctx, b.ID, status); err != nil {
				return err
			}
			audit("base."+sub, b.Handle)
			fmt.Fprintf(out, "Base %s is %s.\n", b.Handle, status)
			return nil
		}
		return fmt.Errorf("there is no Base called %s (launchway bases list shows them)", words[0])
	}
	return usageErr("launchway bases list|create|suspend|resume")
}
