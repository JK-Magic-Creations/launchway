package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const runnersUsage = `Usage:
  launchway runners list [--json]                   runners, their pools and what they hold
  launchway runners join-token --pool NAME          make a join token for a pool, shown once
                     [--uses N] [--ttl 1h] [--labels a,b] [--ephemeral] [--description TEXT]
  launchway runners revoke-token ID                 stop a join token from enrolling runners
  launchway runners revoke RUNNER [--reason TEXT]   revoke an enrolled runner at once
  launchway runners rotate RUNNER                   ask an enrolled runner to move to a new key

Making join tokens, revoking and rotating need the role runner_admin names
(admin by default) on every project.
`

func cmdRunners(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, runnersUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.runnersList(args[1:])
		case "join-token":
			return cl.joinToken(args[1:])
		case "revoke-token":
			return cl.oneID(args[1:], "revoke-token", "/api/v1/runner-join-tokens/%s/revoke", "Join token %s revoked.")
		case "revoke":
			return cl.revokeRunner(args[1:])
		case "rotate":
			return cl.oneID(args[1:], "rotate", "/api/v1/runners/%s/rotate", "Runner %s will move to a new key when it next gets an access token.")
		}
		return usageErr("unknown command runners %s\n\n%s", args[0], runnersUsage)
	})
}

func (cl *client) runnersList(args []string) error {
	fs := flag.NewFlagSet("runners list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var list []struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Pool     string   `json:"pool"`
		Auth     string   `json:"auth"`
		Version  string   `json:"version"`
		Online   bool     `json:"online"`
		Labels   []string `json:"labels"`
		Identity *struct {
			RevokedAt int64 `json:"revoked_at"`
		} `json:"identity"`
		Work []struct {
			Project string `json:"project"`
			Number  int    `json:"number"`
			Step    string `json:"step"`
		} `json:"work"`
	}
	if err := cl.call("GET", "/api/v1/runners", nil, &list); err != nil {
		return err
	}
	return cl.print(list, *asJSON, func() {
		if len(list) == 0 {
			fmt.Fprintln(cl.out, "No runners.")
		}
		for _, r := range list {
			state := "offline"
			switch {
			case r.Identity != nil && r.Identity.RevokedAt != 0:
				state = "revoked"
			case r.Online:
				state = "online"
			}
			auth := map[string]string{"identity": "enrolled", "shared": "shared token (no identity)", "pool": "pool token (no identity)"}[r.Auth]
			fmt.Fprintf(cl.out, "%s  %-8s pool %-10s %s, %s", r.ID, state, r.Pool, auth, nonEmptyStr(r.Version, "version unknown"))
			if len(r.Labels) > 0 {
				fmt.Fprintf(cl.out, ", labels %s", strings.Join(r.Labels, ","))
			}
			for _, w := range r.Work {
				fmt.Fprintf(cl.out, "\n    running %s #%d %s", w.Project, w.Number, w.Step)
			}
			fmt.Fprintln(cl.out)
		}
	})
}

func nonEmptyStr(s, f string) string {
	if s == "" {
		return f
	}
	return s
}

func (cl *client) joinToken(args []string) error {
	fs := flag.NewFlagSet("runners join-token", flag.ContinueOnError)
	pool := fs.String("pool", "default", "the pool the runners join")
	uses := fs.Int("uses", 1, "how many runners may enrol with it")
	ttl := fs.Duration("ttl", time.Hour, "how long it may be used, at most 168h")
	labels := fs.String("labels", "", "labels given to the runners, separated by commas")
	eph := fs.Bool("ephemeral", false, "the runners are revoked when they leave, as pods that enrol at start should be")
	desc := fs.String("description", "", "what it is for")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	body := map[string]any{"pool": *pool, "max_uses": *uses, "ttl": ttl.String(), "ephemeral": *eph, "description": *desc}
	if *labels != "" {
		body["labels"] = strings.Split(*labels, ",")
	}
	var out struct {
		Token     string `json:"token"`
		JoinToken struct {
			ID        string `json:"id"`
			Pool      string `json:"pool"`
			MaxUses   int    `json:"max_uses"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"join_token"`
	}
	if err := cl.call("POST", "/api/v1/runner-join-tokens", body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		t := out.JoinToken
		fmt.Fprintf(cl.out, "Join token %s for pool %s, %d use(s), until %s. It is shown only now:\n\n  %s\n\n",
			t.ID, t.Pool, t.MaxUses, time.UnixMilli(t.ExpiresAt).Format("2006-01-02 15:04"), out.Token)
		fmt.Fprintf(cl.out, "On the runner's machine:\n\n  launchway runner --server %s --join %s\n", cl.c.Server, out.Token)
	})
}

func (cl *client) oneID(args []string, name, path, done string) error {
	fs := flag.NewFlagSet("runners "+name, flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("runners %s takes one ID\n\n%s", name, runnersUsage)
	}
	if err := cl.call("POST", fmt.Sprintf(path, pos[0]), map[string]any{}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, done+"\n", pos[0])
	return nil
}

func (cl *client) revokeRunner(args []string) error {
	fs := flag.NewFlagSet("runners revoke", flag.ContinueOnError)
	reason := fs.String("reason", "", "why, for the audit trail")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("runners revoke takes one runner ID\n\n%s", runnersUsage)
	}
	var out struct {
		Released int `json:"released"`
	}
	if err := cl.call("POST", "/api/v1/runners/"+pos[0]+"/revoke", map[string]string{"reason": *reason}, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Runner %s revoked; %d step(s) it held were queued again.\n", pos[0], out.Released)
	return nil
}
