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
	"github.com/jk-magic-creations/launchway/internal/localauth"
)

const usersHelp = `launchway users: built-in accounts, from the server's own console.

  launchway users list                 the accounts and their state
  launchway users setup-code           a new one-time setup code, before the first administrator exists
  launchway users reset NAME           a one-time link that lets NAME choose a new password and second factor
  launchway users unlock NAME          end a lock after failed sign-ins

These run on the server (or in its pod: kubectl exec -it deploy/launchway -- launchway users ...)
and need only the bootstrap file's database address; no one has to be signed in,
which is the point: this is the way back in when no e-mail or provider exists. Each
use is written to the audit trail as "console". Options: --config FILE (default launchway.yaml).
`

func cmdUsers(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usersHelp)
		return nil
	}
	fs := flag.NewFlagSet("users", flag.ContinueOnError)
	cfgPath := fs.String("config", "launchway.yaml", "the server's bootstrap file")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // the flag set has printed its help
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
	eng, err := engine.Open(engine.Options{Database: cfg.DatabaseAddr(), ConnectWait: wait, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return err
	}
	defer eng.Close()
	return runUsers(eng, args[0], fs.Args(), os.Stdout, time.Now())
}

// runUsers is the commands of `launchway users`, on an open engine.
func runUsers(eng *engine.Engine, sub string, rest []string, out io.Writer, now time.Time) error {
	who := "console"
	if u := os.Getenv("USER"); u != "" {
		who += ":" + u
	}
	audit := func(action, detail string) {
		_ = eng.Audit(engine.AuditEntry{At: now.UnixMilli(), User: who, Source: "console", Action: action, Outcome: "ok", Detail: detail})
	}
	switch sub {
	case "list":
		as, err := eng.ListLocalAccounts()
		if err != nil {
			return err
		}
		if len(as) == 0 {
			fmt.Fprintln(out, "No built-in accounts yet. Open the address in a browser to set up the first administrator (`launchway users setup-code` prints a code).")
		}
		for _, a := range as {
			state := "active"
			switch {
			case a.Disabled:
				state = "disabled"
			case a.PasswordHash == "":
				state = "invited"
			case a.LockedUntil > now.UnixMilli():
				state = "locked"
			}
			fmt.Fprintf(out, "%-24s %-9s %-9s second factor: %v\n", a.Username, a.Role, state, a.TOTPConfirmed)
		}
		return nil
	case "setup-code":
		code := localauth.NewSetupCode()
		if err := eng.IssueSetupCode(localauth.HashCode(code), now.UnixMilli(), now.Add(localauth.DefaultSetupTTL).UnixMilli(), "console"); err != nil {
			return fmt.Errorf("%w (a setup code exists only until the first administrator has been created; use `launchway users reset NAME` for someone locked out)", err)
		}
		audit("auth.setup_code", "a new setup code was issued at the console")
		fmt.Fprintf(out, "Setup code (valid 24 hours, works once; the one issued before no longer works):\n\n    %s\n\nOpen the address of this installation in a browser and enter it.\n", code)
		return nil
	case "reset", "unlock":
		if len(rest) != 1 {
			return usageErr("launchway users %s NAME", sub)
		}
		name := strings.ToLower(rest[0])
		a, err := eng.LocalAccountByName(name)
		if err != nil {
			return fmt.Errorf("there is no built-in account called %s (launchway users list shows them)", name)
		}
		if sub == "unlock" {
			if err := eng.UnlockLocal(name); err != nil {
				return err
			}
			audit("user.unlock", name)
			fmt.Fprintf(out, "%s can try signing in again.\n", name)
			return nil
		}
		token := localauth.NewInviteToken()
		if err := eng.CreateLink(engine.Link{Hash: localauth.HashCode(token), Username: name, Kind: engine.LinkReset, Role: a.Role,
			CreatedAt: now.UnixMilli(), CreatedBy: who, ExpiresAt: now.Add(24 * time.Hour).UnixMilli()}); err != nil {
			return err
		}
		audit("user.reset", name)
		fmt.Fprintf(out, "One-time reset link for %s (valid 24 hours, works once):\n\n    <your address>/accept?token=%s\n\nThey choose a new password and set up a second factor again; their old sessions end when they finish.\n", name, token)
		return nil
	}
	return usageErr("unknown: launchway users %s (list, setup-code, reset NAME, unlock NAME)", sub)
}
