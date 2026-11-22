package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const serviceAccountsUsage = `Usage:
  launchway service-accounts list [--json]                    the service accounts (administrators)
  launchway service-accounts create NAME [--role R]           make a service account: no password, no sign-in
                                 [--description D]            R is viewer (the default), operator or admin
  launchway service-accounts update NAME [--role R] [--disabled=true|false]
  launchway service-accounts delete NAME                      remove it and every token it owns
  launchway service-accounts token NAME --name N              make a token for the account (shown once)
                                 [--repo OWNER/NAME|OWNER/* ...] [--access read|write]
                                 [--from CIDR ...] [--days N] [--role R]

A service account owns the tokens of one automation, so that none is issued from a person's or a
break-glass account. A token with --repo reaches those hosted repositories and nothing else; --from
limits the addresses it may be used from. Only an administrator's signed-in session makes a service
account's tokens (the API token of an administrator may too, only where
auth.tokens.service_accounts_from_tokens is on). Tokens always expire: --days defaults to 90 and
never exceeds the server's maximum.
`

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func cmdServiceAccounts(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, serviceAccountsUsage)
			return cliErr{exitUsage, ""}
		}
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Print(serviceAccountsUsage)
			return nil
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.serviceAccountsList(args[1:])
		case "create":
			return cl.serviceAccountsCreate(args[1:])
		case "update":
			return cl.serviceAccountsUpdate(args[1:])
		case "delete":
			return cl.serviceAccountsDelete(args[1:])
		case "token":
			return cl.serviceAccountsToken(args[1:])
		}
		return usageErr("launchway service-accounts list|create|update|delete|token")
	})
}

func (cl *client) serviceAccountsList(args []string) error {
	fs := flag.NewFlagSet("service-accounts list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var out struct {
		ServiceAccounts []struct {
			Name, Description, Role string
			Disabled                bool
		} `json:"service_accounts"`
	}
	if err := cl.call("GET", "/api/v1/service-accounts", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.ServiceAccounts) == 0 {
			fmt.Fprintln(cl.out, "No service accounts.")
		}
		for _, a := range out.ServiceAccounts {
			state := ""
			if a.Disabled {
				state = "  (switched off)"
			}
			fmt.Fprintf(cl.out, "%-24s %-9s %s%s\n", a.Name, a.Role, a.Description, state)
		}
	})
}

func (cl *client) serviceAccountsCreate(args []string) error {
	fs := flag.NewFlagSet("service-accounts create", flag.ContinueOnError)
	role := fs.String("role", "viewer", "viewer, operator or admin")
	desc := fs.String("description", "", "what the account is for")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway service-accounts create NAME [--role R] [--description D]")
	}
	if err := cl.call("POST", "/api/v1/service-accounts", map[string]string{"name": pos[0], "role": *role, "description": *desc}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Made the service account %s (%s). Make its token with: launchway service-accounts token %s --name N\n", pos[0], *role, pos[0])
	return nil
}

func (cl *client) serviceAccountsUpdate(args []string) error {
	fs := flag.NewFlagSet("service-accounts update", flag.ContinueOnError)
	role := fs.String("role", "", "viewer, operator or admin")
	disabled := fs.String("disabled", "", "true to switch it off, false to switch it on")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*role == "" && *disabled == "") {
		return usageErr("launchway service-accounts update NAME [--role R] [--disabled=true|false]")
	}
	body := map[string]any{}
	if *role != "" {
		body["role"] = *role
	}
	if *disabled != "" {
		body["disabled"] = *disabled == "true"
	}
	if err := cl.call("PATCH", "/api/v1/service-accounts/"+pos[0], body, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Changed %s.\n", pos[0])
	return nil
}

func (cl *client) serviceAccountsDelete(args []string) error {
	fs := flag.NewFlagSet("service-accounts delete", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway service-accounts delete NAME")
	}
	if err := cl.call("DELETE", "/api/v1/service-accounts/"+pos[0], nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Removed %s and its tokens.\n", pos[0])
	return nil
}

func (cl *client) serviceAccountsToken(args []string) error {
	fs := flag.NewFlagSet("service-accounts token", flag.ContinueOnError)
	name := fs.String("name", "", "the token's name")
	days := fs.Int("days", 0, "days until it expires (default 90)")
	role := fs.String("role", "", "limit the token's role (no higher than the account's)")
	access := fs.String("access", "", "read or write, for the repositories")
	var repos, from listFlag
	fs.Var(&repos, "repo", "a hosted repository OWNER/NAME, or OWNER/* (repeatable)")
	fs.Var(&from, "from", "an address range the token may be used from (repeatable)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *name == "" {
		return usageErr("launchway service-accounts token NAME --name N [--repo R ...] [--access read|write] [--from CIDR ...] [--days N]")
	}
	body := map[string]any{"name": *name, "expires_in_days": *days, "role": *role, "repositories": []string(repos), "repo_access": *access, "source_ranges": []string(from)}
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := cl.call("POST", "/api/v1/service-accounts/"+pos[0]+"/tokens", body, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Token %s for %s (shown once; store it in your secret store):\n%s\n", out.ID, pos[0], out.Token)
	return nil
}
