package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/settings"
)

const variablesUsage = `Usage:
  launchway variables list      LEVEL [--environment E] [--json]   the variables at a level, with what each inherits
  launchway variables effective FLIGHT [--environment E] [--json] every variable in effect for a Flight, with its chain
  launchway variables run       RUN [--json]                       the variables a run used, as recorded
  launchway variables set    NAME LEVEL --type T --value V [--of KIND] [--environment E] [--description D]
  launchway variables delete NAME LEVEL [--environment E]
  launchway variables lock   NAME LEVEL --reason R [--environment E]
  launchway variables unlock NAME LEVEL [--environment E]

LEVEL is one of: --installation, --organization, --unit NAME or --project NAME.
Types: string, number, boolean, list (a,b,c or a JSON list), map (JSON), secret
(store:key#field, a reference and never the secret) and connection (a name, with --of).
Use --json-value to give the value as JSON. The nearest setting wins: a nearer level
overrides a wider one, and a lock set at a level beats everything below it.
Setting needs an operator of the Flight, or an administrator of the Squadron, the Wing
or the Base; locking needs the level's administrator. Changes are audited.
`

func cmdVariables(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, variablesUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.variablesList(args[1:])
		case "effective":
			return cl.variablesEffective(args[1:])
		case "run":
			return cl.variablesRun(args[1:])
		case "set":
			return cl.variablesSet(args[1:])
		case "delete":
			return cl.variablesDelete(args[1:])
		case "lock":
			return cl.variablesLock(args[1:])
		case "unlock":
			return cl.variablesUnlock(args[1:])
		}
		return usageErr("unknown command variables %s\n\n%s", args[0], variablesUsage)
	})
}

// levelFlags are the flags that say where.
type levelFlags struct {
	installation, organization *bool
	unit, project, env         *string
}

func addLevelFlags(fs *flag.FlagSet) levelFlags {
	return levelFlags{
		installation: fs.Bool("installation", false, "the whole installation"),
		organization: fs.Bool("organization", false, "the Wing (organization)"),
		unit:         fs.String("unit", "", "a Squadron (unit), by name"),
		project:      fs.String("project", "", "a Flight, by name"),
		env:          fs.String("environment", "", "only for this Airfield"),
	}
}

func (l levelFlags) level() (level, scope string, err error) {
	n := 0
	for _, b := range []bool{*l.installation, *l.organization, *l.unit != "", *l.project != ""} {
		if b {
			n++
		}
	}
	switch {
	case n != 1:
		return "", "", usageErr("say the level: exactly one of --installation, --organization, --unit NAME, --project NAME")
	case *l.installation:
		return "installation", "", nil
	case *l.organization:
		return "organization", "", nil
	case *l.unit != "":
		return "unit", *l.unit, nil
	}
	return "project", *l.project, nil
}

func (l levelFlags) query() (string, error) {
	level, scope, err := l.level()
	if err != nil {
		return "", err
	}
	return "?" + url.Values{"level": {level}, "scope": {scope}, "environment": {*l.env}}.Encode(), nil
}

func (cl *client) variablesList(args []string) error {
	fs := flag.NewFlagSet("variables list", flag.ContinueOnError)
	lf := addLevelFlags(fs)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q, err := lf.query()
	if err != nil {
		return err
	}
	var v settings.VariableView
	if err := cl.call("GET", "/api/v1/variables"+q, nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() { printVariables(cl, &v) })
}

func (cl *client) variablesEffective(args []string) error {
	fs := flag.NewFlagSet("variables effective", flag.ContinueOnError)
	env := fs.String("environment", "", "for this Airfield")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway variables effective FLIGHT [--environment E] [--json]")
	}
	path := "/api/v1/projects/" + url.PathEscape(pos[0]) + "/variables"
	if *env != "" {
		path += "?environment=" + url.QueryEscape(*env)
	}
	var v settings.VariableView
	if err := cl.call("GET", path, nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() { printVariables(cl, &v) })
}

func sourceText(s settings.Source) string {
	switch s.Kind {
	case settings.SourceLevel:
		t := string(s.Level)
		if s.Scope != "" && s.Level != settings.LevelOrganization {
			t += " " + s.Scope
		}
		if s.Environment != "" {
			t += " for " + s.Environment
		}
		return t
	}
	return s.Kind
}

func printVariables(cl *client, v *settings.VariableView) {
	if len(v.Variables) == 0 {
		fmt.Fprintln(cl.out, "No variables here yet. Set one with: launchway variables set NAME ...")
	}
	for _, e := range v.Variables {
		lock := ""
		if e.Locked != nil {
			lock = fmt.Sprintf("  [locked by %s: %s]", sourceText(settings.Source{Kind: settings.SourceLevel, Level: e.Locked.Level, Scope: e.Locked.Scope}), e.Locked.Reason)
		}
		fmt.Fprintf(cl.out, "%s = %s  (%s, from %s)%s\n", e.Name, e.Display, e.Type, sourceText(e.From), lock)
		for _, l := range e.Chain[1:] {
			note := "overridden"
			if l.Ignored != "" {
				note = l.Ignored
			}
			fmt.Fprintf(cl.out, "    ~~%s~~  %s  (%s)\n", l.Display, sourceText(l.Source), note)
		}
	}
	fmt.Fprintln(cl.out, "\n"+v.Rule)
}

// valueFor turns what a person typed into the JSON a type takes.
func valueFor(typ, v, raw string) (json.RawMessage, error) {
	if raw != "" {
		return json.RawMessage(raw), nil
	}
	b := func(x any) (json.RawMessage, error) { out, err := json.Marshal(x); return out, err }
	switch typ {
	case "number":
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, usageErr("%q is not a number", v)
		}
		return b(f)
	case "boolean":
		if v != "true" && v != "false" {
			return nil, usageErr("a boolean is true or false")
		}
		return json.RawMessage(v), nil
	case "list":
		if strings.HasPrefix(strings.TrimSpace(v), "[") {
			return json.RawMessage(v), nil
		}
		var items []string
		for _, s := range strings.Split(v, ",") {
			items = append(items, strings.TrimSpace(s))
		}
		return b(items)
	case "map":
		return json.RawMessage(v), nil
	case "secret":
		if strings.HasPrefix(strings.TrimSpace(v), "{") {
			return json.RawMessage(v), nil
		}
		store, rest, ok := strings.Cut(v, ":")
		key, field, _ := strings.Cut(rest, "#")
		if !ok || store == "" || key == "" {
			return nil, usageErr("a secret is a reference: store:key or store:key#field")
		}
		return b(map[string]string{"store": store, "key": key, "field": field})
	}
	return b(v)
}

func (cl *client) variablesSet(args []string) error {
	fs := flag.NewFlagSet("variables set", flag.ContinueOnError)
	lf := addLevelFlags(fs)
	typ := fs.String("type", "string", "string, number, boolean, list, map, secret or connection")
	of := fs.String("of", "", "for a connection, what kind (registry, cluster, ...)")
	val := fs.String("value", "", "the value")
	raw := fs.String("json-value", "", "the value as JSON")
	desc := fs.String("description", "", "one line saying what it is for")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	level, scope, lerr := lf.level()
	if len(pos) != 1 || lerr != nil || (*val == "" && *raw == "") {
		return usageErr("launchway variables set NAME (--installation|--organization|--unit U|--project P) --type T --value V")
	}
	value, err := valueFor(*typ, *val, *raw)
	if err != nil {
		return err
	}
	body := map[string]any{"level": level, "scope": scope, "environment": *lf.env, "type": *typ, "of": *of, "value": value, "description": *desc}
	var out settings.SetResult
	if err := cl.call("PUT", "/api/v1/variables/"+url.PathEscape(pos[0]), body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if out.Changed {
			fmt.Fprintf(cl.out, "Set %s at %s.\n", pos[0], level)
		} else {
			fmt.Fprintf(cl.out, "%s is already that at %s; nothing changed.\n", pos[0], level)
		}
	})
}

func (cl *client) variablesKeyed(name string, args []string, method, suffix, past string, extra func(fs *flag.FlagSet) func() (map[string]any, error)) error {
	fs := flag.NewFlagSet("variables "+name, flag.ContinueOnError)
	lf := addLevelFlags(fs)
	asJSON := fs.Bool("json", false, "print as JSON")
	var body func() (map[string]any, error)
	if extra != nil {
		body = extra(fs)
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway variables %s NAME (--installation|--organization|--unit U|--project P)", name)
	}
	level, scope, err := lf.level()
	if err != nil {
		return err
	}
	path := "/api/v1/variables/" + url.PathEscape(pos[0]) + suffix
	var in any
	if body != nil {
		m, err := body()
		if err != nil {
			return err
		}
		m["level"], m["scope"], m["environment"] = level, scope, *lf.env
		in = m
	} else {
		path += "?" + url.Values{"level": {level}, "scope": {scope}, "environment": {*lf.env}}.Encode()
	}
	var out map[string]any
	if err := cl.call(method, path, in, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s %s at %s.\n", strings.ToUpper(past[:1])+past[1:], pos[0], level)
		if ov, ok := out["overrides"].([]any); ok && len(ov) > 0 {
			fmt.Fprintf(cl.out, "%d nearer setting(s) are kept and now ignored; they count again when the lock is removed.\n", len(ov))
		}
	})
}

func (cl *client) variablesDelete(args []string) error {
	return cl.variablesKeyed("delete", args, "DELETE", "", "deleted", nil)
}

func (cl *client) variablesUnlock(args []string) error {
	return cl.variablesKeyed("unlock", args, "DELETE", "/lock", "unlocked", nil)
}

func (cl *client) variablesLock(args []string) error {
	var reason *string
	return cl.variablesKeyed("lock", args, "PUT", "/lock", "locked", func(fs *flag.FlagSet) func() (map[string]any, error) {
		reason = fs.String("reason", "", "why: shown wherever the lock stops someone")
		return func() (map[string]any, error) {
			if *reason == "" {
				return nil, usageErr("a lock needs --reason")
			}
			return map[string]any{"reason": *reason}, nil
		}
	})
}

func (cl *client) variablesRun(args []string) error {
	fs := flag.NewFlagSet("variables run", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway variables run RUN [--json]")
	}
	var out struct {
		RunID     string                      `json:"run_id"`
		RerunOf   string                      `json:"rerun_of"`
		Variables []settings.RecordedVariable `json:"variables"`
	}
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/variables", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Variables) == 0 {
			fmt.Fprintln(cl.out, "This Sortie used no variables.")
		}
		v := settings.VariableView{Variables: nil}
		for _, r := range out.Variables {
			v.Variables = append(v.Variables, settings.VarEntry{Resolved: r.Resolved})
		}
		if out.RerunOf != "" {
			fmt.Fprintf(cl.out, "A re-run: it used the variables of %s.\n", out.RerunOf)
		}
		printVariables(cl, &v)
	})
}
