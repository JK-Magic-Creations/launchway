package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const keysUsage = `Usage:
  launchway keys list [--json]                           your SSH keys, with when and from where each was last used
  launchway keys add --file KEY.pub [--name N]           add a public key for Git over SSH
                     [--expires 2027-06-30T00:00:00Z]    (ed25519, ECDSA or RSA of at least 3072 bits)
  launchway keys delete ID                               delete one of your keys; it stops working at once
  launchway keys host [--json]                           the server's SSH address and host key fingerprints, to verify on first connect

Git hosting must be turned on, and Git over SSH for the connection itself (forge.ssh.listen).
A key belongs to one owner: you, or one repository as a deploy key (launchway repos deploy-keys).
`

type keyView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Fingerprint  string `json:"fingerprint"`
	CanWrite     bool   `json:"can_write"`
	ExpiresAt    string `json:"expires_at"`
	LastUsedAt   string `json:"last_used_at"`
	LastUsedFrom string `json:"last_used_from"`
}

func (k keyView) line() string {
	s := fmt.Sprintf("%s  %s  %s  (%s)", k.ID, k.Name, k.Fingerprint, k.Type)
	if k.CanWrite {
		s += "  may push"
	}
	if k.ExpiresAt != "" {
		s += "  expires " + k.ExpiresAt
	}
	if k.LastUsedAt != "" {
		s += "  last used " + k.LastUsedAt + " from " + k.LastUsedFrom
	} else {
		s += "  never used"
	}
	return s
}

// cmdHosting runs the commands of Git hosting: repos and keys.
func cmdHosting(name string, args []string) error {
	switch name {
	case "keys":
		return cmdKeys(args)
	case "pr":
		return cmdPR(args)
	case "encryption":
		return cmdEncryption(args)
	}
	return cmdRepos(args)
}

func cmdKeys(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, keysUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.keysList(args[1:])
		case "add":
			return cl.keysAdd(args[1:])
		case "delete":
			return cl.keysDelete(args[1:])
		case "host":
			return cl.keysHost(args[1:])
		}
		return usageErr("unknown command keys %s\n\n%s", args[0], keysUsage)
	})
}

func printKeys(cl *client, items []keyView, none string) {
	if len(items) == 0 {
		fmt.Fprintln(cl.out, none)
	}
	for _, k := range items {
		fmt.Fprintln(cl.out, k.line())
	}
}

func (cl *client) keysList(args []string) error {
	fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var page struct {
		Items []keyView `json:"items"`
	}
	if err := cl.call("GET", "/api/v1/user/ssh-keys", nil, &page); err != nil {
		return err
	}
	return cl.print(page, *asJSON, func() {
		printKeys(cl, page.Items, "You have no SSH keys yet. Add one with: launchway keys add --file ~/.ssh/id_ed25519.pub")
	})
}

func readKeyFile(path string) (string, error) {
	if path == "" {
		return "", usageErr("say which public key: --file KEY.pub")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the person named the file of their own public key
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (cl *client) keysAdd(args []string) error {
	fs := flag.NewFlagSet("keys add", flag.ContinueOnError)
	file := fs.String("file", "", "the public key file (.pub)")
	name := fs.String("name", "", "a name for the key (default: its comment)")
	exp := fs.String("expires", "", "when the key stops working, RFC 3339")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	pub, err := readKeyFile(*file)
	if err != nil {
		return err
	}
	var k keyView
	if err := cl.call("POST", "/api/v1/user/ssh-keys", map[string]any{"name": *name, "public_key": pub, "expires_at": *exp}, &k); err != nil {
		return err
	}
	return cl.print(k, *asJSON, func() { fmt.Fprintln(cl.out, "Added "+k.line()) })
}

func (cl *client) keysDelete(args []string) error {
	fs := flag.NewFlagSet("keys delete", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway keys delete ID")
	}
	if err := cl.call("DELETE", "/api/v1/user/ssh-keys/"+url.PathEscape(pos[0]), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Deleted %s. It no longer opens any repository.\n", pos[0])
	return nil
}

func (cl *client) keysHost(args []string) error {
	fs := flag.NewFlagSet("keys host", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var info struct {
		Enabled      bool   `json:"enabled"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
		Certificates bool   `json:"certificates"`
		HostKeys     []struct {
			Type        string `json:"type"`
			Fingerprint string `json:"fingerprint"`
		} `json:"host_keys"`
	}
	if err := cl.call("GET", "/api/v1/ssh", nil, &info); err != nil {
		return err
	}
	return cl.print(info, *asJSON, func() {
		if !info.Enabled {
			fmt.Fprintln(cl.out, "Git over SSH is off on this server (installation setting forge.ssh.listen). Use HTTPS.")
			return
		}
		fmt.Fprintf(cl.out, "Git over SSH: %s port %d\n", info.Host, info.Port)
		for _, h := range info.HostKeys {
			fmt.Fprintf(cl.out, "  %s  %s\n", h.Fingerprint, h.Type)
		}
		fmt.Fprintln(cl.out, "Compare the fingerprint ssh shows the first time you connect with one of these.")
	})
}

// ---- deploy keys and protected branches (launchway repos ...) ----

const reposKeysUsage = `
  launchway repos deploy-keys list OWNER/NAME [--json]   a repository's deploy keys (its administrators)
  launchway repos deploy-keys add OWNER/NAME --file KEY.pub [--name N] [--write] [--expires T]
                                                         a key that opens this one repository, read-only unless --write
  launchway repos deploy-keys delete OWNER/NAME ID       delete a deploy key
  launchway repos protected-branches OWNER/NAME [--set main,release/*]
                                                         show, or replace, the branches that take no direct push except from administrators
  launchway repos protected-tags OWNER/NAME [--rule 'v*:create=admin,change=none,pipelines=ship-*'] [--clear]
                                                         show, or replace (repeat --rule), the tags that are protected
`

func (cl *client) deployKeys(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway repos deploy-keys list|add|delete OWNER/NAME ...%s", reposKeysUsage)
	}
	fs := flag.NewFlagSet("repos deploy-keys "+args[0], flag.ContinueOnError)
	file := fs.String("file", "", "the public key file (.pub)")
	name := fs.String("name", "", "a name for the key")
	write := fs.Bool("write", false, "let the key push")
	exp := fs.String("expires", "", "when the key stops working, RFC 3339")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usageErr("launchway repos deploy-keys %s OWNER/NAME ...", args[0])
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	base += "/deploy-keys"
	switch args[0] {
	case "list":
		var page struct {
			Items []keyView `json:"items"`
		}
		if err := cl.call("GET", base, nil, &page); err != nil {
			return err
		}
		return cl.print(page, *asJSON, func() { printKeys(cl, page.Items, "This repository has no deploy keys.") })
	case "add":
		pub, err := readKeyFile(*file)
		if err != nil {
			return err
		}
		var k keyView
		if err := cl.call("POST", base, map[string]any{"name": *name, "public_key": pub, "write": *write, "expires_at": *exp}, &k); err != nil {
			return err
		}
		return cl.print(k, *asJSON, func() { fmt.Fprintln(cl.out, "Added "+k.line()) })
	case "delete":
		if len(pos) != 2 {
			return usageErr("launchway repos deploy-keys delete OWNER/NAME ID")
		}
		if err := cl.call("DELETE", base+"/"+url.PathEscape(pos[1]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Deleted %s.\n", pos[1])
		return nil
	}
	return usageErr("unknown command repos deploy-keys %s", args[0])
}

func (cl *client) protectedBranches(args []string) error {
	fs := flag.NewFlagSet("repos protected-branches", flag.ContinueOnError)
	set := fs.String("set", "", "replace the patterns with these, comma separated (an empty value removes the protection)")
	asJSON := fs.Bool("json", false, "print as JSON")
	var writers writerFlag
	fs.Var(&writers, "writer", "admit a pipeline to a protected branch: BRANCH=FLIGHTS=FILES, each list comma separated (repeat; none removes all rules)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos protected-branches OWNER/NAME [--set main,release/*] [--writer main=mcp-*=apps/mcp-servers/base.yaml]")
	}
	path, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	path += "/protected-branches"
	var out struct {
		Patterns        []string       `json:"patterns"`
		PipelineWriters []pipelineRule `json:"pipeline_writers"`
	}
	keep := false
	if visited(fs)["writer"] && !visited(fs)["set"] {
		// Only the pipeline rules change: keep the patterns as they are.
		if err := cl.call("GET", path, nil, &out); err != nil {
			return err
		}
		*set = strings.Join(out.Patterns, ",")
		keep = true
	}
	if visited(fs)["set"] || keep {
		var pats []string
		for _, p := range strings.Split(*set, ",") {
			if p = strings.TrimSpace(p); p != "" {
				pats = append(pats, p)
			}
		}
		if pats == nil {
			pats = []string{}
		}
		body := map[string]any{"patterns": pats}
		if visited(fs)["writer"] {
			body["pipeline_writers"] = writers.rules
		}
		err = cl.call("PUT", path, body, &out)
	} else {
		err = cl.call("GET", path, nil, &out)
	}
	if err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Patterns) == 0 {
			fmt.Fprintln(cl.out, "No protected branches.")
			return
		}
		fmt.Fprintln(cl.out, "Protected branches (direct pushes from administrators only, never deleted): "+strings.Join(out.Patterns, ", "))
		for _, w := range out.PipelineWriters {
			fmt.Fprintf(cl.out, "Pipelines admitted to %s: Flights %s may change only %s\n", w.Branch, strings.Join(w.Projects, ", "), strings.Join(w.Files, ", "))
		}
	})
}

// pipelineRule is a rule that admits pipelines to a protected branch.
type pipelineRule struct {
	Branch   string   `json:"branch"`
	Projects []string `json:"projects"`
	Files    []string `json:"files"`
}

// writerFlag collects --writer BRANCH=PROJECTS=FILES.
type writerFlag struct{ rules []pipelineRule }

func (w *writerFlag) String() string { return "" }

func (w *writerFlag) Set(v string) error {
	if v == "none" {
		w.rules = []pipelineRule{}
		return nil
	}
	parts := strings.Split(v, "=")
	if len(parts) != 3 {
		return fmt.Errorf("write --writer BRANCH=FLIGHTS=FILES, for example main=mcp-*=apps/mcp-servers/base.yaml (lists are comma separated)")
	}
	split := func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	w.rules = append(w.rules, pipelineRule{Branch: strings.TrimSpace(parts[0]), Projects: split(parts[1]), Files: split(parts[2])})
	return nil
}

// tagRuleFlag collects --rule PATTERN[:create=ROLE,change=ROLE,pipelines=A|B] flags.
type tagRuleFlag struct{ rules []tagRuleOut }

type tagRuleOut struct {
	Pattern    string   `json:"pattern"`
	CreateRole string   `json:"create_role"`
	ChangeRole string   `json:"change_role"`
	Pipelines  []string `json:"pipelines"`
}

func (f *tagRuleFlag) String() string { return "" }

func (f *tagRuleFlag) Set(v string) error {
	pat, opts, _ := strings.Cut(v, ":")
	r := tagRuleOut{Pattern: strings.TrimSpace(pat), Pipelines: []string{}}
	for _, o := range strings.Split(opts, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(o), "=")
		switch k {
		case "":
		case "create":
			r.CreateRole = val
		case "change":
			if val != "none" {
				r.ChangeRole = val
			}
		case "pipelines":
			for _, p := range strings.Split(val, "|") {
				if p = strings.TrimSpace(p); p != "" {
					r.Pipelines = append(r.Pipelines, p)
				}
			}
		default:
			return fmt.Errorf("%q is not create=, change= or pipelines=", o)
		}
	}
	f.rules = append(f.rules, r)
	return nil
}

func (cl *client) protectedTags(args []string) error {
	fs := flag.NewFlagSet("repos protected-tags", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	removeAll := fs.Bool("clear", false, "remove the protection")
	var rules tagRuleFlag
	fs.Var(&rules, "rule", "a protected tag: PATTERN[:create=admin|operator,change=none|admin,pipelines=FLIGHT|FLIGHT] (repeat; replaces the rules)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos protected-tags OWNER/NAME [--rule 'v*:create=admin,change=none,pipelines=ship-*'] [--clear]")
	}
	path, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	path += "/protected-tags"
	var out struct {
		Rules []tagRuleOut `json:"rules"`
	}
	if *removeAll || len(rules.rules) > 0 {
		body := map[string]any{"rules": rules.rules}
		if rules.rules == nil {
			body["rules"] = []tagRuleOut{}
		}
		err = cl.call("PUT", path, body, &out)
	} else {
		err = cl.call("GET", path, nil, &out)
	}
	if err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Rules) == 0 {
			fmt.Fprintln(cl.out, "No protected tags.")
			return
		}
		for _, r := range out.Rules {
			change := "nobody may move or delete one"
			if r.ChangeRole != "" {
				change = "the " + r.ChangeRole + " role may move or delete one"
			}
			line := fmt.Sprintf("%s: the %s role may make one; %s", r.Pattern, r.CreateRole, change)
			if len(r.Pipelines) > 0 {
				line += "; Flights admitted to make one: " + strings.Join(r.Pipelines, ", ")
			}
			fmt.Fprintln(cl.out, line)
		}
	})
}
