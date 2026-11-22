package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const signingUsage = `Usage:
  launchway signing keys list [--json]                  your signing keys (the SSH keys your commits and tags are signed with)
  launchway signing keys add --file KEY.pub [--name N]  register a signing key, so what it signs shows as verified
                             [--expires 2027-06-30T00:00:00Z]
  launchway signing keys revoke ID                      revoke a key from now on; commits dated after show as signed by a revoked key
  launchway signing keys delete ID                      delete a key; what it signed shows as signed by an unknown key
  launchway signing verify OWNER/NAME OBJECT...         whether commits or annotated tags (full object names) are verified, and why
                           [--json]
  launchway signing installation-key [--json]           the key Launchway signs its own merge and suggestion commits with

To sign with SSH: git config gpg.format ssh; git config user.signingkey ~/.ssh/id_ed25519.pub; git config commit.gpgsign true.
Verified commits must be turned on with Git hosting.
`

type signingKeyView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   string `json:"expires_at"`
	RevokedAt   string `json:"revoked_at"`
}

func (k signingKeyView) line() string {
	s := fmt.Sprintf("%s  %s  %s  (%s)", k.ID, k.Name, k.Fingerprint, k.Type)
	if k.ExpiresAt != "" {
		s += "  expires " + k.ExpiresAt
	}
	if k.RevokedAt != "" {
		s += "  revoked " + k.RevokedAt
	}
	return s
}

func cmdSigning(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, signingUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "keys":
			if len(args) < 2 {
				return usageErr("%s", signingUsage)
			}
			switch args[1] {
			case "list":
				return cl.signingKeysList(args[2:])
			case "add":
				return cl.signingKeysAdd(args[2:])
			case "revoke":
				return cl.signingKeysChange("revoke", args[2:])
			case "delete":
				return cl.signingKeysChange("delete", args[2:])
			}
		case "verify":
			return cl.signingVerify(args[1:])
		case "installation-key":
			return cl.signingInstallationKey(args[1:])
		}
		return usageErr("unknown command signing %s\n\n%s", strings.Join(args, " "), signingUsage)
	})
}

func (cl *client) signingKeysList(args []string) error {
	fs := flag.NewFlagSet("signing keys list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var page struct {
		Items []signingKeyView `json:"items"`
	}
	if err := cl.call("GET", "/api/v1/user/signing-keys", nil, &page); err != nil {
		return err
	}
	return cl.print(page, *asJSON, func() {
		if len(page.Items) == 0 {
			fmt.Fprintln(cl.out, "You have no signing keys yet. Add one with: launchway signing keys add --file ~/.ssh/id_ed25519.pub")
		}
		for _, k := range page.Items {
			fmt.Fprintln(cl.out, k.line())
		}
	})
}

func (cl *client) signingKeysAdd(args []string) error {
	fs := flag.NewFlagSet("signing keys add", flag.ContinueOnError)
	file := fs.String("file", "", "the public key file (.pub)")
	name := fs.String("name", "", "a name for the key (default: its comment)")
	exp := fs.String("expires", "", "when the key stops counting, RFC 3339")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	pub, err := readKeyFile(*file)
	if err != nil {
		return err
	}
	var k signingKeyView
	if err := cl.call("POST", "/api/v1/user/signing-keys", map[string]any{"name": *name, "public_key": pub, "expires_at": *exp}, &k); err != nil {
		return err
	}
	return cl.print(k, *asJSON, func() { fmt.Fprintln(cl.out, "Added "+k.line()) })
}

func (cl *client) signingKeysChange(what string, args []string) error {
	pos, err := parseArgs(flag.NewFlagSet("signing keys "+what, flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway signing keys %s ID", what)
	}
	if what == "delete" {
		if err := cl.call("DELETE", "/api/v1/user/signing-keys/"+url.PathEscape(pos[0]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Deleted %s. What it signed now shows as signed by an unknown key.\n", pos[0])
		return nil
	}
	var k signingKeyView
	if err := cl.call("POST", "/api/v1/user/signing-keys/"+url.PathEscape(pos[0])+"/revoke", nil, &k); err != nil {
		return err
	}
	fmt.Fprintln(cl.out, "Revoked "+k.line())
	return nil
}

func (cl *client) signingVerify(args []string) error {
	fs := flag.NewFlagSet("signing verify", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 || !strings.Contains(pos[0], "/") {
		return usageErr("launchway signing verify OWNER/NAME OBJECT...")
	}
	var out struct {
		Items []struct {
			Object      string `json:"object"`
			Kind        string `json:"kind"`
			State       string `json:"state"`
			Explanation string `json:"explanation"`
		} `json:"items"`
	}
	if err := cl.call("GET", "/api/v1/repositories/"+pathEscapeParts(pos[0])+"/signatures?shas="+url.QueryEscape(strings.Join(pos[1:], ",")), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		for _, r := range out.Items {
			fmt.Fprintf(cl.out, "%s  %s  %s\n    %s\n", r.Object, r.Kind, r.State, r.Explanation)
		}
	})
}

func (cl *client) signingInstallationKey(args []string) error {
	fs := flag.NewFlagSet("signing installation-key", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var k struct {
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
		Address     string `json:"address"`
	}
	if err := cl.call("GET", "/api/v1/signing/installation-key", nil, &k); err != nil {
		return err
	}
	return cl.print(k, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s\n%s\nPublished at %s\n", k.PublicKey, k.Fingerprint, k.Address)
	})
}

func pathEscapeParts(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
