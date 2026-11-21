package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const eventSourcesUsage = `Usage:
  launchway event-sources list PROJECT [--json]
  launchway event-sources create PROJECT --name NAME [--map INPUT=EXPRESSION ...] [--filter EXPR] [--dedupe-key EXPR]
                                [--schema-file FILE] [--ref REF] [--debounce SECONDS | --batch SECONDS]
                                [--max-body BYTES] [--rate PER_MINUTE]     make a source; its signing secret is shown once
  launchway event-sources change PROJECT NAME [the same flags] [--enable=false] [--rotate-secret]
  launchway event-sources delete PROJECT NAME
  launchway event-sources test PROJECT NAME --body FILE|-          judge a body as a call would be; nothing starts, nothing is recorded

An event source is an address other systems call to start a run. The caller signs the body with HMAC-SHA256
over "<timestamp>.<body>" (X-Launchway-Timestamp and X-Launchway-Signature: sha256=<hex>). Expressions are
written over body, headers and source, for example --map version=body.release.version
--map environment='default(body.env, "dev")' --filter 'body.action == "released"'.
Making a source needs the operator role and a root key (launchway encryption init).
`

func cmdEventSources(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, eventSourcesUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.sourcesList(args[1:])
		case "create", "change":
			return cl.sourcesWrite(args[0], args[1:])
		case "delete":
			return cl.sourcesDelete(args[1:])
		case "test":
			return cl.sourcesTest(args[1:])
		}
		return usageErr("unknown command event-sources %s\n\n%s", args[0], eventSourcesUsage)
	})
}

type sourceRow struct {
	engine.EventSource
	URL string `json:"url"`
}

func (cl *client) sourcesList(args []string) error {
	fs := flag.NewFlagSet("event-sources list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which project: launchway event-sources list PROJECT")
	}
	var list []sourceRow
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/event-sources", nil, &list); err != nil {
		return err
	}
	return cl.print(list, *asJSON, func() {
		if len(list) == 0 {
			fmt.Fprintln(cl.out, "No event sources.")
		}
		for _, s := range list {
			state := ""
			if !s.Enabled {
				state = " (off)"
			}
			fmt.Fprintf(cl.out, "%s%s  %s\n", s.Name, state, s.URL)
			keys := make([]string, 0, len(s.Mapping))
			for k := range s.Mapping {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(cl.out, "    %s = %s\n", k, s.Mapping[k])
			}
			if s.Filter != "" {
				fmt.Fprintf(cl.out, "    filter: %s\n", s.Filter)
			}
			if s.DedupeKey != "" {
				fmt.Fprintf(cl.out, "    dedupe key: %s\n", s.DedupeKey)
			}
		}
	})
}

func (cl *client) sourcesWrite(verb string, args []string) error {
	fs := flag.NewFlagSet("event-sources "+verb, flag.ContinueOnError)
	name := fs.String("name", "", "the source's name")
	filter := fs.String("filter", "", "an expression that must be true")
	dedupe := fs.String("dedupe-key", "", "an expression: events with the same key are duplicates")
	schemaFile := fs.String("schema-file", "", "a JSON Schema the body must match")
	ref := fs.String("ref", "", "the branch or tag whose pipeline runs (default: the default branch)")
	debounce := fs.Int("debounce", 0, "start once, this many seconds after the calls stop")
	batch := fs.Int("batch", 0, "start once, this many seconds after the first call")
	maxBody := fs.Int("max-body", 0, "the largest body in bytes")
	rate := fs.Int("rate", 0, "calls a minute")
	enable := fs.Bool("enable", true, "false turns the source off")
	rotate := fs.Bool("rotate-secret", false, "make a new signing secret (shown once)")
	var maps stringList
	fs.Var(&maps, "map", "INPUT=EXPRESSION (repeat for each input)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	in := map[string]any{}
	path := ""
	switch verb {
	case "create":
		if len(pos) != 1 || *name == "" {
			return usageErr("say the project and --name\n\n%s", eventSourcesUsage)
		}
		in["name"] = *name
		path = "/api/v1/projects/" + url.PathEscape(pos[0]) + "/event-sources"
	default:
		if len(pos) != 2 {
			return usageErr("say the project and the source: launchway event-sources change PROJECT NAME [flags]")
		}
		path = "/api/v1/projects/" + url.PathEscape(pos[0]) + "/event-sources/" + url.PathEscape(pos[1])
		if *rotate {
			in["rotate_secret"] = true
		}
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if len(maps) > 0 {
		m := map[string]string{}
		for _, kv := range maps {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" || v == "" {
				return usageErr("--map takes INPUT=EXPRESSION, not %q", kv)
			}
			m[k] = v
		}
		in["mapping"] = m
	}
	for flagName, key := range map[string]string{"filter": "filter", "dedupe-key": "dedupe_key", "ref": "ref"} {
		if set[flagName] {
			in[key] = map[string]string{"filter": *filter, "dedupe-key": *dedupe, "ref": *ref}[flagName]
		}
	}
	for flagName, key := range map[string]string{"debounce": "debounce_seconds", "batch": "batch_seconds", "max-body": "max_body", "rate": "rate_per_minute"} {
		if set[flagName] {
			in[key] = map[string]int{"debounce": *debounce, "batch": *batch, "max-body": *maxBody, "rate": *rate}[flagName]
		}
	}
	if set["enable"] {
		in["enabled"] = *enable
	}
	if *schemaFile != "" {
		b, err := os.ReadFile(*schemaFile) //nolint:gosec // G703: the person's own flag, read as the person
		if err != nil {
			return err
		}
		in["schema"] = string(b)
	}
	var out struct {
		Source sourceRow `json:"source"`
		Secret string    `json:"secret"`
	}
	if err := cl.call("POST", path, in, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Event source %s: %s\n", out.Source.Name, out.Source.URL)
	if out.Secret != "" {
		fmt.Fprintf(cl.out, "\nSigning secret (shown only now; keep it in your secret store):\n  %s\n\nSign each call: HMAC-SHA256 of \"<timestamp>.<body>\" with the secret, sent as\n  X-Launchway-Timestamp: <Unix seconds>\n  X-Launchway-Signature: sha256=<hex>\n", out.Secret)
	}
	return nil
}

func (cl *client) sourcesDelete(args []string) error {
	fs := flag.NewFlagSet("event-sources delete", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("say the project and the source: launchway event-sources delete PROJECT NAME")
	}
	if err := cl.call("DELETE", "/api/v1/projects/"+url.PathEscape(pos[0])+"/event-sources/"+url.PathEscape(pos[1]), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Deleted the event source %s.\n", pos[1])
	return nil
}

func (cl *client) sourcesTest(args []string) error {
	fs := flag.NewFlagSet("event-sources test", flag.ContinueOnError)
	bodyFile := fs.String("body", "", "a file with the JSON body, or - for standard input")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *bodyFile == "" {
		return usageErr("say the project, the source and --body\n\n%s", eventSourcesUsage)
	}
	var b []byte
	if *bodyFile == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(*bodyFile)
	}
	if err != nil {
		return err
	}
	if !json.Valid(b) {
		return usageErr("the body is not JSON")
	}
	var res struct {
		Status   string            `json:"status"`
		Reason   string            `json:"reason"`
		Problems []string          `json:"problems"`
		Inputs   map[string]string `json:"inputs"`
		Key      string            `json:"dedupe_key"`
	}
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/event-sources/"+url.PathEscape(pos[1])+"/test", map[string]any{"body": json.RawMessage(b)}, &res); err != nil {
		return err
	}
	return cl.print(res, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s: %s\n", res.Status, res.Reason)
		for _, p := range res.Problems {
			fmt.Fprintf(cl.out, "  problem: %s\n", p)
		}
		keys := make([]string, 0, len(res.Inputs))
		for k := range res.Inputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(cl.out, "  input %s = %s\n", k, res.Inputs[k])
		}
		if res.Key != "" {
			fmt.Fprintf(cl.out, "  dedupe key: %s\n", res.Key)
		}
	})
}
