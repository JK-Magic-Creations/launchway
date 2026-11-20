package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const configUsage = `Usage:
  launchway config overview [--json]                  the kinds of objects, the levels and their names
  launchway config list KIND [--deleted] [--json]     objects of a kind: projects, libraries, environments,
                                                      runner-pools, role-bindings, policies, policy-sources,
                                                      health-sources, models, secret-stores, units,
                                                      installation, organisation
  launchway config get KIND NAME [--json]             one object, as YAML (or JSON)
  launchway config set KIND NAME -f SPEC.yaml         create or replace an object; - reads standard input
                     [--unit U] [--version N] [--comment TEXT] [--manager terraform]
  launchway config create KIND NAME -f SPEC.yaml      create an object; fails if it exists
  launchway config delete KIND NAME [--version N] [--comment TEXT]   delete (it can be restored)
  launchway config restore KIND NAME                  bring a deleted object back
  launchway config history [KIND [NAME]] [--import ID] [--limit N] [--json]
  launchway config validate -f CHANGES.yaml           check changes without making them
  launchway config apply -f CHANGES.yaml              make several changes, all or none
  launchway config export [-o FILE]                   every object as configuration text (YAML)
  launchway config import FILE [--prune] [--apply]    show what importing FILE would change; --apply makes it so
  launchway config imports [--json]                   past imports, the first start's among them
  launchway config effective PROJECT [--json]         what applies to a project, and where it comes from

  launchway health-sources|models|policy-sources get|set|create|delete|restore|history ...   likewise
  launchway projects list|get|set|create|delete|restore|history ...   the same for projects

A spec is the object's settings with the keys of the configuration file, for
example a project's repo, branch and pipeline. CHANGES.yaml is a list of
{kind, name, unit, spec, action (put, delete or restore), version}.
Changing configuration needs the admin role on every project.
`

func cmdConfig(args []string) error {
	return runClient(func() error {
		if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
			fmt.Fprint(os.Stderr, configUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).config(args, "")
	})
}

// cmdKindAlias is launchway projects ...: launchway config ... projects.
func cmdKindAlias(kind string) func([]string) error {
	return func(args []string) error {
		return runClient(func() error {
			if len(args) == 0 {
				fmt.Fprint(os.Stderr, configUsage)
				return cliErr{exitUsage, ""}
			}
			c, err := loadCredentials()
			if err != nil {
				return err
			}
			return newClient(c, os.Stdout).config(args, kind)
		})
	}
}

// config runs a config subcommand; kind, if given, is the kind the
// command line leaves out (launchway projects get shop).
func (cl *client) config(args []string, kind string) error {
	cmd, rest := args[0], args[1:]
	withKind := func() []string {
		if kind != "" {
			return append([]string{kind}, rest...)
		}
		return rest
	}
	switch cmd {
	case "overview":
		return cl.configOverview(rest)
	case "list":
		return cl.configList(withKind())
	case "get", "show":
		return cl.configGet(withKind())
	case "set", "put":
		return cl.configSet(withKind(), false)
	case "create":
		return cl.configSet(withKind(), true)
	case "delete":
		return cl.configLifecycle(withKind(), "delete")
	case "restore":
		return cl.configLifecycle(withKind(), "restore")
	case "history":
		return cl.configHistory(withKind())
	case "validate":
		return cl.configChanges(rest, true)
	case "apply":
		return cl.configChanges(rest, false)
	case "export":
		return cl.configExport(rest)
	case "import":
		return cl.configImport(rest)
	case "imports":
		return cl.configImports(rest)
	case "effective":
		return cl.configEffective(rest)
	}
	return usageErr("unknown command config %s\n\n%s", cmd, configUsage)
}

// callWith is call with request headers.
func (cl *client) callWith(method, path string, body, v any, headers map[string]string) error {
	old := cl.http.Transport
	cl.http.Transport = headerTransport{base: old, headers: headers}
	defer func() { cl.http.Transport = old }()
	return cl.call(method, path, body, v)
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// raw fetches a path's body as it is.
func (cl *client) raw(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", cl.c.Server+path, nil)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	req.Header.Set("User-Agent", "launchway-cli/"+version)
	resp, err := cl.http.Do(req)
	if err != nil {
		return nil, apiErr("cannot reach %s: %v", cl.c.Server, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return nil, apiErr("%s", nonEmptyStr(e.Error, strings.TrimSpace(string(data))))
	}
	return data, nil
}

type cfgObject struct {
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Unit      string          `json:"unit"`
	Spec      json.RawMessage `json:"spec"`
	Version   int64           `json:"version"`
	ManagedBy string          `json:"managed_by"`
	UpdatedAt int64           `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
	Deleted   bool            `json:"deleted"`
}

func (cl *client) configOverview(args []string) error {
	fs := flag.NewFlagSet("config overview", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var v struct {
		Available bool `json:"available"`
		Kinds     []struct {
			Path, Title, Doc string
		} `json:"kinds"`
		Levels map[string]struct {
			Singular string `json:"singular"`
			Plural   string `json:"plural"`
		} `json:"levels"`
		UnitsInUse bool `json:"units_in_use"`
		Status     struct {
			Revision int64  `json:"revision"`
			Problem  string `json:"problem"`
		} `json:"status"`
	}
	if err := cl.call("GET", "/api/v1/config", nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		if !v.Available {
			fmt.Fprintln(cl.out, "This server keeps its configuration in its file, not in the database.")
			return
		}
		fmt.Fprintf(cl.out, "Configuration revision %d.", v.Status.Revision)
		if v.Status.Problem != "" {
			fmt.Fprintf(cl.out, " Not applied on this server: %s", v.Status.Problem)
		}
		fmt.Fprintln(cl.out)
		fmt.Fprintf(cl.out, "Levels: %s > %s > %s > %s", v.Levels["installation"].Singular, v.Levels["organisation"].Singular,
			v.Levels["unit"].Singular, v.Levels["project"].Singular)
		if !v.UnitsInUse {
			fmt.Fprintf(cl.out, " (no %s yet)", strings.ToLower(v.Levels["unit"].Plural))
		}
		fmt.Fprintln(cl.out)
		for _, k := range v.Kinds {
			fmt.Fprintf(cl.out, "  %-16s %s\n", k.Path, k.Doc)
		}
	})
}

func (cl *client) configList(args []string) error {
	fs := flag.NewFlagSet("config list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	deleted := fs.Bool("deleted", false, "list deleted objects too")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("give the kind: launchway config list projects")
	}
	q := ""
	if *deleted {
		q = "?deleted=1"
	}
	var v struct {
		Objects []cfgObject `json:"objects"`
	}
	if err := cl.call("GET", "/api/v1/config/"+url.PathEscape(pos[0])+q, nil, &v); err != nil {
		return err
	}
	return cl.print(v.Objects, *asJSON, func() {
		if len(v.Objects) == 0 {
			fmt.Fprintf(cl.out, "No %s.\n", pos[0])
		}
		for _, o := range v.Objects {
			extra := ""
			if o.Unit != "" {
				extra += "  unit " + o.Unit
			}
			if o.Deleted {
				extra += "  (deleted)"
			}
			fmt.Fprintf(cl.out, "%-30s v%-4d %-9s %s by %s%s\n", o.Name, o.Version, o.ManagedBy, when(o.UpdatedAt), o.UpdatedBy, extra)
		}
	})
}

func when(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}

func (cl *client) configGet(args []string) error {
	fs := flag.NewFlagSet("config get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("give the kind and the name: launchway config get projects shop")
	}
	var o cfgObject
	if err := cl.call("GET", "/api/v1/config/"+url.PathEscape(pos[0])+"/"+url.PathEscape(pos[1]), nil, &o); err != nil {
		return err
	}
	if *asJSON {
		return cl.print(o, true, nil)
	}
	var spec any
	_ = json.Unmarshal(o.Spec, &spec)
	doc := map[string]any{"kind": o.Kind, "name": o.Name, "version": o.Version, "managed_by": o.ManagedBy, "spec": spec}
	if o.Unit != "" {
		doc["unit"] = o.Unit
	}
	if o.Deleted {
		doc["deleted"] = true
	}
	return writeYAML(cl.out, doc)
}

func writeYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	defer enc.Close()
	return enc.Encode(v)
}

// readSpec reads a YAML or JSON file (- for standard input) into JSON.
func readSpec(path string) (json.RawMessage, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, usageErr("%v", err)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, usageErr("%s: %v", path, err)
	}
	b, err := json.Marshal(jsonable(v))
	if err != nil {
		return nil, usageErr("%s: %v", path, err)
	}
	return b, nil
}

// jsonable turns what YAML reads into what JSON can write.
func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = jsonable(x)
		}
		return t
	case map[any]any:
		out := map[string]any{}
		for k, x := range t {
			out[fmt.Sprint(k)] = jsonable(x)
		}
		return out
	case []any:
		for i, x := range t {
			t[i] = jsonable(x)
		}
	}
	return v
}

func (cl *client) configSet(args []string, create bool) error {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	file := fs.String("f", "", "the spec, as YAML or JSON (- for standard input)")
	inline := fs.String("spec", "", "the spec as JSON, instead of -f")
	unit := fs.String("unit", "", "the unit it belongs to (a project), or is defined at (anything else)")
	ver := fs.Int64("version", 0, "change it only if it is at this version")
	comment := fs.String("comment", "", "why, for the history")
	mgr := fs.String("manager", "", "who manages it: api (the default) or terraform")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("give the kind and the name: launchway config set projects shop -f shop.yaml")
	}
	var spec json.RawMessage
	switch {
	case *file != "" && *inline != "":
		return usageErr("give the spec with -f or --spec, not both")
	case *file != "":
		if spec, err = readSpec(*file); err != nil {
			return err
		}
	case *inline != "":
		spec = json.RawMessage(*inline)
	default:
		spec = json.RawMessage("{}")
	}
	body := map[string]any{"unit": *unit, "spec": spec, "comment": *comment}
	headers := map[string]string{}
	if *ver > 0 {
		headers["If-Match"] = strconv.Quote(strconv.FormatInt(*ver, 10))
	}
	if *mgr != "" {
		headers["X-Launchway-Manager"] = *mgr
	}
	var res struct {
		Object  cfgObject `json:"object"`
		Changed bool      `json:"changed"`
		Change  *struct {
			Action  string `json:"action"`
			Version int64  `json:"version"`
		} `json:"change"`
	}
	method, path := "PUT", "/api/v1/config/"+url.PathEscape(pos[0])+"/"+url.PathEscape(pos[1])
	if create {
		method, path = "POST", "/api/v1/config/"+url.PathEscape(pos[0])
		body["name"] = pos[1]
	}
	if err := cl.callWith(method, path, body, &res, headers); err != nil {
		return err
	}
	return cl.print(res, *asJSON, func() {
		if !res.Changed {
			fmt.Fprintf(cl.out, "%s %s is already so (version %d); nothing changed.\n", pos[0], pos[1], res.Object.Version)
			return
		}
		fmt.Fprintf(cl.out, "%s %s: %s, now version %d.\n", pos[0], pos[1], res.Change.Action, res.Object.Version)
	})
}

func (cl *client) configLifecycle(args []string, action string) error {
	fs := flag.NewFlagSet("config "+action, flag.ContinueOnError)
	ver := fs.Int64("version", 0, "only if it is at this version")
	comment := fs.String("comment", "", "why, for the history")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("give the kind and the name: launchway config %s projects shop", action)
	}
	headers := map[string]string{}
	if *ver > 0 {
		headers["If-Match"] = strconv.Quote(strconv.FormatInt(*ver, 10))
	}
	path := "/api/v1/config/" + url.PathEscape(pos[0]) + "/" + url.PathEscape(pos[1])
	method := "DELETE"
	if action == "restore" {
		method, path = "POST", path+"/restore"
	}
	var body any
	if *comment != "" {
		body = map[string]string{"comment": *comment}
	}
	var res struct {
		Object cfgObject `json:"object"`
	}
	if err := cl.callWith(method, path, body, &res, headers); err != nil {
		return err
	}
	if action == "delete" {
		fmt.Fprintf(cl.out, "%s %s deleted (version %d); launchway config restore %s %s brings it back.\n", pos[0], pos[1], res.Object.Version, pos[0], pos[1])
	} else {
		fmt.Fprintf(cl.out, "%s %s restored (version %d).\n", pos[0], pos[1], res.Object.Version)
	}
	return nil
}

type cfgChange struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Version    int64  `json:"version"`
	Action     string `json:"action"`
	Actor      string `json:"actor"`
	At         int64  `json:"at"`
	ImportID   string `json:"import_id"`
	Comment    string `json:"comment"`
	UnitBefore string `json:"unit_before"`
	UnitAfter  string `json:"unit_after"`
	Diff       []struct {
		Path   string `json:"path"`
		Before any    `json:"before"`
		After  any    `json:"after"`
	} `json:"diff"`
}

func (cl *client) configHistory(args []string) error {
	fs := flag.NewFlagSet("config history", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	imp := fs.String("import", "", "only the changes of this import")
	limit := fs.Int("limit", 50, "at most this many")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	path := "/api/v1/config/history"
	q := url.Values{}
	switch len(pos) {
	case 0:
	case 1:
		q.Set("kind", pos[0])
	case 2:
		path = "/api/v1/config/" + url.PathEscape(pos[0]) + "/" + url.PathEscape(pos[1]) + "/history"
	default:
		return usageErr("launchway config history [KIND [NAME]]")
	}
	if *imp != "" {
		q.Set("import", *imp)
	}
	q.Set("limit", strconv.Itoa(*limit))
	var v struct {
		Changes []cfgChange `json:"changes"`
	}
	if err := cl.call("GET", path+"?"+q.Encode(), nil, &v); err != nil {
		return err
	}
	return cl.print(v.Changes, *asJSON, func() {
		if len(v.Changes) == 0 {
			fmt.Fprintln(cl.out, "No changes.")
		}
		for _, c := range v.Changes {
			fmt.Fprintf(cl.out, "%s  %s %s v%d %s by %s", when(c.At), c.Kind, c.Name, c.Version, c.Action, c.Actor)
			if c.ImportID != "" {
				fmt.Fprintf(cl.out, " (import %s)", c.ImportID)
			}
			if c.Comment != "" {
				fmt.Fprintf(cl.out, ": %s", c.Comment)
			}
			fmt.Fprintln(cl.out)
			for _, d := range c.Diff {
				fmt.Fprintf(cl.out, "    %s: %s -> %s\n", d.Path, short(d.Before), short(d.After))
			}
		}
	})
}

func short(v any) string {
	if v == nil {
		return "(unset)"
	}
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

// configChanges validates or applies a list of changes from a file.
func (cl *client) configChanges(args []string, dry bool) error {
	name := "apply"
	if dry {
		name = "validate"
	}
	fs := flag.NewFlagSet("config "+name, flag.ContinueOnError)
	file := fs.String("f", "", "the changes, as YAML or JSON (- for standard input)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return usageErr("give the changes with -f FILE")
	}
	raw, err := readSpec(*file)
	if err != nil {
		return err
	}
	var changes []map[string]any
	if err := json.Unmarshal(raw, &changes); err != nil {
		var wrapped struct {
			Changes []map[string]any `json:"changes"`
		}
		if err2 := json.Unmarshal(raw, &wrapped); err2 != nil || wrapped.Changes == nil {
			return usageErr("%s: a list of changes ({kind, name, spec, ...}) is expected", *file)
		}
		changes = wrapped.Changes
	}
	body := map[string]any{"changes": changes}
	if dry {
		var v struct {
			Valid    bool     `json:"valid"`
			Problems []string `json:"problems"`
		}
		if err := cl.call("POST", "/api/v1/config/validate", body, &v); err != nil {
			return err
		}
		if v.Valid {
			fmt.Fprintf(cl.out, "The %d change(s) are valid.\n", len(changes))
			return nil
		}
		return apiErr("the changes would not be valid:\n  %s", strings.Join(v.Problems, "\n  "))
	}
	var v struct {
		Results []struct {
			Object  cfgObject `json:"object"`
			Changed bool      `json:"changed"`
		} `json:"results"`
	}
	if err := cl.call("POST", "/api/v1/config/apply", body, &v); err != nil {
		return err
	}
	for _, r := range v.Results {
		state := "unchanged"
		if r.Changed {
			state = fmt.Sprintf("now version %d", r.Object.Version)
		}
		fmt.Fprintf(cl.out, "%s %s: %s\n", r.Object.Kind, r.Object.Name, state)
	}
	return nil
}

func (cl *client) configExport(args []string) error {
	fs := flag.NewFlagSet("config export", flag.ContinueOnError)
	out := fs.String("o", "", "write to this file instead of standard output")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	data, err := cl.raw("/api/v1/config/export")
	if err != nil {
		return err
	}
	if *out == "" {
		_, err := cl.out.Write(data)
		return err
	}
	if err := os.WriteFile(*out, data, 0o600); err != nil {
		return usageErr("%v", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s.\n", *out)
	return nil
}

type planItem struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Unit   string `json:"unit"`
	Diff   []struct {
		Path   string `json:"path"`
		Before any    `json:"before"`
		After  any    `json:"after"`
	} `json:"diff"`
}

func (cl *client) configImport(args []string) error {
	fs := flag.NewFlagSet("config import", flag.ContinueOnError)
	prune := fs.Bool("prune", false, "also delete objects the file does not describe")
	apply := fs.Bool("apply", false, "make the changes (without it, only show them)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("give the file: launchway config import launchway.yaml")
	}
	var data []byte
	if pos[0] == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(pos[0])
	}
	if err != nil {
		return usageErr("%v", err)
	}
	var plan struct {
		Plan struct {
			Items    []planItem `json:"items"`
			Digest   string     `json:"digest"`
			Changes  int        `json:"changes"`
			Problems []string   `json:"problems"`
		} `json:"plan"`
		Applied bool `json:"applied"`
		Import  *struct {
			ID string `json:"id"`
		} `json:"import"`
	}
	body := map[string]any{"text": string(data), "prune": *prune}
	if err := cl.call("POST", "/api/v1/config/import", body, &plan); err != nil {
		return err
	}
	show := func() {
		if plan.Plan.Changes == 0 {
			fmt.Fprintln(cl.out, "Nothing to change: the database already holds what the file says.")
		}
		for _, it := range plan.Plan.Items {
			if it.Action == "unchanged" {
				continue
			}
			sign := map[string]string{"create": "+", "update": "~", "delete": "-"}[it.Action]
			fmt.Fprintf(cl.out, "%s %s %s\n", sign, it.Kind, it.Name)
			for _, d := range it.Diff {
				fmt.Fprintf(cl.out, "    %s: %s -> %s\n", d.Path, short(d.Before), short(d.After))
			}
		}
		if len(plan.Plan.Problems) > 0 {
			fmt.Fprintf(cl.out, "\nThe import would be refused:\n  %s\n", strings.Join(plan.Plan.Problems, "\n  "))
		}
	}
	if !*apply {
		if err := cl.print(plan.Plan, *asJSON, func() {
			show()
			if plan.Plan.Changes > 0 && len(plan.Plan.Problems) == 0 {
				fmt.Fprintf(cl.out, "\n%d change(s). Run again with --apply to make them.\n", plan.Plan.Changes)
			}
		}); err != nil {
			return err
		}
		if len(plan.Plan.Problems) > 0 {
			return cliErr{exitAPI, ""}
		}
		return nil
	}
	if len(plan.Plan.Problems) > 0 {
		show()
		return cliErr{exitAPI, "the import was refused"}
	}
	body["apply"], body["digest"], body["origin"] = true, plan.Plan.Digest, "launchway config import "+pos[0]
	if err := cl.call("POST", "/api/v1/config/import", body, &plan); err != nil {
		return err
	}
	return cl.print(plan, *asJSON, func() {
		show()
		fmt.Fprintf(cl.out, "\nImported (%s): %d change(s).\n", plan.Import.ID, plan.Plan.Changes)
	})
}

func (cl *client) configImports(args []string) error {
	fs := flag.NewFlagSet("config imports", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var v struct {
		Imports []struct {
			ID      string          `json:"id"`
			At      int64           `json:"at"`
			Actor   string          `json:"actor"`
			Origin  string          `json:"origin"`
			Summary json.RawMessage `json:"summary"`
		} `json:"imports"`
	}
	if err := cl.call("GET", "/api/v1/config/imports", nil, &v); err != nil {
		return err
	}
	return cl.print(v.Imports, *asJSON, func() {
		if len(v.Imports) == 0 {
			fmt.Fprintln(cl.out, "No imports.")
		}
		for _, i := range v.Imports {
			fmt.Fprintf(cl.out, "%s  %s  %s by %s\n", i.ID, when(i.At), i.Origin, i.Actor)
		}
	})
}

func (cl *client) configEffective(args []string) error {
	fs := flag.NewFlagSet("config effective", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("give the project: launchway config effective shop")
	}
	var v struct {
		Project string `json:"project"`
		Unit    string `json:"unit"`
		Objects []struct {
			Kind, Name, From, Unit string
		} `json:"objects"`
		Settings []struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
			From  string `json:"from"`
		} `json:"settings"`
	}
	if err := cl.call("GET", "/api/v1/config/effective/"+url.PathEscape(pos[0]), nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		fmt.Fprintf(cl.out, "Project %s", v.Project)
		if v.Unit != "" {
			fmt.Fprintf(cl.out, " (unit %s)", v.Unit)
		}
		fmt.Fprintln(cl.out)
		sort.SliceStable(v.Objects, func(i, j int) bool { return v.Objects[i].Kind < v.Objects[j].Kind })
		for _, o := range v.Objects {
			from := o.From
			if o.Unit != "" {
				from += " " + o.Unit
			}
			fmt.Fprintf(cl.out, "  %-14s %-28s from the %s\n", o.Kind, o.Name, from)
		}
		for _, s := range v.Settings {
			fmt.Fprintf(cl.out, "  %-24s %-10s (%s)\n", s.Key, short(s.Value), s.From)
		}
	})
}

// configVerb reports whether word is a command that changes or reads one
// configuration object, for the commands that began as read-only lists
// (health-sources, models, policy) and now also manage their kind.
func configVerb(word string) bool {
	switch word {
	case "get", "show", "set", "put", "create", "delete", "restore", "history", "list":
		return true
	}
	return false
}
