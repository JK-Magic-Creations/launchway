package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

const accountUsage = `Usage:
  launchway account show [--json]                 who you are, your SSH keys, API tokens (with their limits and expiry),
                                                  second factor and signed-in browsers, and which optional areas this server has
  launchway account sessions [--json]             your signed-in browsers; the one you are using is marked
  launchway account revoke SESSION_ID             end one of your signed-in browsers (the id is from account sessions)
  launchway account revoke --others               end all the others (from a token: all of them)
  launchway account profile NAME [--json]         a person's public face: name, and what they did lately on repositories you may read
  launchway squadrons team NAME [--json]          a Squadron's members by role, repositories and Flights

Nothing private is in a profile: no address, groups, roles, tokens or sign-in times, and a repository you cannot read
adds nothing to anyone's activity as you see it.
`

func cmdAccount(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, accountUsage)
			return cliErr{exitUsage, ""}
		}
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Print(accountUsage)
			return nil
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "show":
			return cl.accountShow(args[1:])
		case "sessions":
			return cl.accountSessions(args[1:])
		case "revoke":
			return cl.accountRevoke(args[1:])
		case "profile":
			return cl.accountProfile(args[1:])
		}
		return usageErr("unknown command account %s\n\n%s", args[0], accountUsage)
	})
}

func day(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04")
}

func (cl *client) accountShow(args []string) error {
	fs := flag.NewFlagSet("account show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var out struct {
		Profile  struct{ User, DisplayName, Email, Via string } `json:"profile"`
		Features map[string]bool                                `json:"features"`
		Tokens   struct {
			Items []struct {
				Name      string
				ExpiresAt int64 `json:"expires_at"`
				LastUsed  int64 `json:"last_used"`
			} `json:"items"`
			MaxDays int `json:"max_days"`
		} `json:"tokens"`
		SSHKeys []struct{ Title, Fingerprint string } `json:"ssh_keys"`
		Second  struct {
			Available, Enabled, Required bool
			RecoveryCodesLeft            int `json:"recovery_codes_left"`
		} `json:"second_factor"`
		Sessions []sessionLine `json:"sessions"`
	}
	if err := cl.call("GET", "/api/v1/account/summary", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s (%s), signed in by %s\n", out.Profile.User, strings.TrimSpace(out.Profile.DisplayName+" "+out.Profile.Email), out.Profile.Via)
		if out.Features["tokens"] {
			fmt.Fprintf(cl.out, "API tokens: %d (at most %d days)\n", len(out.Tokens.Items), out.Tokens.MaxDays)
			for _, t := range out.Tokens.Items {
				fmt.Fprintf(cl.out, "  %s  expires %s, last used %s\n", t.Name, day(t.ExpiresAt), day(t.LastUsed))
			}
		}
		if out.Features["ssh_keys"] {
			fmt.Fprintf(cl.out, "SSH keys: %d\n", len(out.SSHKeys))
		}
		switch {
		case out.Second.Available && out.Second.Enabled:
			fmt.Fprintf(cl.out, "Second factor: on (%d recovery codes left)\n", out.Second.RecoveryCodesLeft)
		case out.Second.Available:
			fmt.Fprintf(cl.out, "Second factor: off%s\n", map[bool]string{true: " (required of you)", false: ""}[out.Second.Required])
		default:
			fmt.Fprintln(cl.out, "Second factor: managed by your identity provider, or not applicable")
		}
		if out.Features["sessions"] {
			fmt.Fprintf(cl.out, "Signed-in browsers: %d\n", len(out.Sessions))
		}
		var on []string
		for k, v := range out.Features {
			if v {
				on = append(on, k)
			}
		}
		sortStrings(on)
		fmt.Fprintf(cl.out, "This server has: %s\n", strings.Join(on, ", "))
	})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

type sessionLine struct {
	ID        string
	CreatedAt int64 `json:"created_at"`
	LastSeen  int64 `json:"last_seen"`
	ExpiresAt int64 `json:"expires_at"`
	Current   bool
}

func (cl *client) accountSessions(args []string) error {
	fs := flag.NewFlagSet("account sessions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var out struct {
		Items []sessionLine `json:"items"`
	}
	if err := cl.call("GET", "/api/v1/account/sessions", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, s := range out.Items {
			mark := ""
			if s.Current {
				mark = "this one"
			}
			fmt.Fprintf(tw, "%s\tsigned in %s\tlast used %s\tends %s\t%s\n", s.ID, day(s.CreatedAt), day(s.LastSeen), day(s.ExpiresAt), mark)
		}
		_ = tw.Flush()
	})
}

func (cl *client) accountRevoke(args []string) error {
	fs := flag.NewFlagSet("account revoke", flag.ContinueOnError)
	others := fs.Bool("others", false, "end every session but this one")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	var out struct{ Ended int }
	switch {
	case *others && len(pos) == 0:
		err = cl.call("POST", "/api/v1/account/sessions/end-others", map[string]any{}, &out)
	case !*others && len(pos) == 1:
		err = cl.call("DELETE", "/api/v1/account/sessions/"+url.PathEscape(pos[0]), nil, &out)
	default:
		return usageErr("launchway account revoke SESSION_ID   or   launchway account revoke --others")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Ended %d.\n", out.Ended)
	return nil
}

func (cl *client) accountProfile(args []string) error {
	fs := flag.NewFlagSet("account profile", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway account profile NAME")
	}
	var out struct {
		Name        string
		DisplayName string `json:"display_name"`
		Activity    struct {
			Items []struct {
				Kind, Repository, Title string
				At                      int64
			}
		}
	}
	if err := cl.call("GET", "/api/v1/people/"+url.PathEscape(pos[0]), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s %s\n", pos[0], out.DisplayName)
		for _, it := range out.Activity.Items {
			fmt.Fprintf(cl.out, "  %s  %s  %s  %s\n", day(it.At), it.Kind, it.Repository, it.Title)
		}
	})
}

func (cl *client) squadronsTeam(args []string) error {
	fs := flag.NewFlagSet("squadrons team", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway squadrons team NAME")
	}
	var out struct {
		Members      map[string][]struct{ Who string }
		Repositories []struct{ Path, Visibility string }
		Flights      []struct{ Name string }
	}
	if err := cl.call("GET", "/api/v1/units/"+url.PathEscape(pos[0])+"/team", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		for _, role := range []string{"admin", "operator", "viewer"} {
			var who []string
			for _, m := range out.Members[role] {
				who = append(who, m.Who)
			}
			fmt.Fprintf(cl.out, "%s: %s\n", role, strings.Join(who, ", "))
		}
		var repos, flights []string
		for _, r := range out.Repositories {
			repos = append(repos, r.Path)
		}
		for _, f := range out.Flights {
			flights = append(flights, f.Name)
		}
		fmt.Fprintf(cl.out, "repositories: %s\nflights: %s\n", strings.Join(repos, ", "), strings.Join(flights, ", "))
	})
}
