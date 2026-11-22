package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// launchway work agents, autonomy, samples, and the agent's own calls.

func (cl *client) workAgents(args []string) error {
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		return cl.workDo("agents list", rest, agList)
	case "create":
		return cl.workDo("agents create", rest, agCreate)
	case "update":
		return cl.workDo("agents update", rest, agUpdate)
	case "disable", "enable":
		return cl.workDo("agents "+verb, rest, agSwitch(verb))
	case "show", "actions", "delete", "rotate-token":
		return cl.workDo("agents "+verb, rest, agNamed(verb))
	case "reviews":
		return cl.workDo("agents reviews", rest, agReviews)
	}
	return usageErr("unknown command work agents %s\n\n%s", verb, workUsage)
}

func (cl *client) agentRows(agents []any) {
	rows := make([][]string, 0, len(agents))
	for _, a := range agents {
		state := "on"
		if wstr(a, "disabled") == "true" {
			state = "OFF"
		}
		rows = append(rows, []string{wstr(a, "name"), state, wstr(a, "scope", "kind") + ":" + wstr(a, "scope", "id"), wstr(a, "owner"), wnames(wlist(a, "projects")), wstr(a, "used_tokens_day"), wstr(a, "used_cents_day")})
	}
	cl.wtable("NAME\tSTATE\tSCOPE\tOWNER\tPROJECTS\tTOKENS TODAY\tCENTS TODAY", rows)
}

func agList(w *wcmd) func([]string) (*workReq, error) {
	return func([]string) (*workReq, error) {
		return &workReq{method: "GET", path: "/api/v1/work/agents", show: func(out any) { w.cl.agentRows(wlist(out, "agents")) }}, nil
	}
}

// agentFlags are the flags that create or change an agent.
type agentFlags struct {
	str      map[string]*string
	ints     map[string]*int64
	mayMerge *bool
	lists    map[string]*multi
}

func (w *wcmd) agentFlags(forUpdate bool) *agentFlags {
	f := &agentFlags{str: map[string]*string{}, ints: map[string]*int64{}, lists: map[string]*multi{}}
	names := []string{"description", "model", "class"}
	if forUpdate {
		names = append(names, "owner")
	}
	for _, n := range names {
		f.str[n] = w.fs.String(n, "", "the agent's "+n)
	}
	for _, n := range []string{"concurrency", "per-day", "budget-tokens-day", "budget-cents-day", "item-tokens", "item-cents", "item-seconds"} {
		f.ints[n] = w.fs.Int64(n, 0, "limit: "+n)
	}
	f.mayMerge = w.fs.Bool("may-merge", false, "whether it may merge what the rules allow")
	for _, n := range []string{"project", "type", "label"} {
		m := &multi{}
		f.lists[n] = m
		w.fs.Var(m, n, n+" it works on (repeat for more)")
	}
	return f
}

// body is the flags that were given, as a request body.
func (f *agentFlags) body(g map[string]bool) map[string]any {
	b := map[string]any{}
	for n, p := range f.str {
		if g[n] {
			b[n] = *p
		}
	}
	for n, p := range f.ints {
		if g[n] {
			b[underscore(n)] = *p
		}
	}
	if g["may-merge"] {
		b["may_merge"] = *f.mayMerge
	}
	for n, m := range f.lists {
		if g[n] {
			b[map[string]string{"project": "projects", "type": "types", "label": "labels"}[n]] = []string(*m)
		}
	}
	return b
}

func underscore(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c == '-' {
			out[i] = '_'
		}
	}
	return string(out)
}

func agCreate(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	f := w.agentFlags(false)
	return func(pos []string) (*workReq, error) {
		sc, err := w.needScope()
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, usageErr("launchway work agents create NAME --scope S [--project OWNER/NAME ...] [flags]")
		}
		b := f.body(w.given())
		b["name"], b["scope"] = pos[0], scopeObject(sc)
		return &workReq{method: "POST", path: "/api/v1/work/agents", body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Registered agent %s.\nIts token, shown once (store it where the agent reads its secrets):\n%s\n", wstr(out, "agent", "name"), wstr(out, "token"))
		}}, nil
	}
}

func agUpdate(w *wcmd) func([]string) (*workReq, error) {
	f := w.agentFlags(true)
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work agents update NAME [flags]")
		}
		b := f.body(w.given())
		if len(b) == 0 {
			return nil, usageErr("say what to change: launchway work agents update NAME --concurrency 2 ...")
		}
		return &workReq{method: "PATCH", path: "/api/v1/work/agents/" + urlPath(pos[0]), body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Changed agent %s.\n", wstr(out, "name"))
		}}, nil
	}
}

// agSwitch is disable (the kill switch) and enable.
func agSwitch(verb string) func(w *wcmd) func([]string) (*workReq, error) {
	return func(w *wcmd) func([]string) (*workReq, error) {
		return func(pos []string) (*workReq, error) {
			if len(pos) != 1 {
				return nil, usageErr("launchway work agents %s NAME", verb)
			}
			return &workReq{method: "PATCH", path: "/api/v1/work/agents/" + urlPath(pos[0]), body: map[string]any{"disabled": verb == "disable"}, show: func(out any) {
				state := "on"
				if wstr(out, "disabled") == "true" {
					state = "off: its token no longer works and its work is freed"
				}
				fmt.Fprintf(w.cl.out, "Agent %s is %s.\n", wstr(out, "name"), state)
			}}, nil
		}
	}
}

// agNamed is show, actions, delete and rotate-token: one agent, one route.
func agNamed(verb string) func(w *wcmd) func([]string) (*workReq, error) {
	return func(w *wcmd) func([]string) (*workReq, error) {
		return func(pos []string) (*workReq, error) {
			if len(pos) != 1 {
				return nil, usageErr("launchway work agents %s NAME", verb)
			}
			base := "/api/v1/work/agents/" + urlPath(pos[0])
			switch verb {
			case "show":
				return &workReq{method: "GET", path: base}, nil
			case "actions":
				return &workReq{method: "GET", path: base + "/actions", show: func(out any) {
					for _, a := range wlist(out, "actions") {
						fmt.Fprintf(w.cl.out, "%v\n", a)
					}
				}}, nil
			case "delete":
				return &workReq{method: "DELETE", path: base, show: func(any) { fmt.Fprintf(w.cl.out, "Agent %s removed.\n", pos[0]) }}, nil
			}
			return &workReq{method: "POST", path: base + "/rotate-token", show: func(out any) {
				fmt.Fprintf(w.cl.out, "New token for %s, shown once (the old one no longer works):\n%s\n", pos[0], wstr(out, "token"))
			}}, nil
		}
	}
}

func agReviews(w *wcmd) func([]string) (*workReq, error) {
	repo := w.fs.String("code-repo", "", "OWNER/NAME of the repository")
	pull := w.fs.String("pull", "", "a pull request's number (default: all)")
	return func([]string) (*workReq, error) {
		if *repo == "" {
			return nil, usageErr("launchway work agents reviews --code-repo OWNER/NAME [--pull N]")
		}
		return &workReq{method: "GET", path: "/api/v1/work/pull-reviews" + q("repo", *repo, "pull", *pull), show: func(out any) {
			rows := [][]string{}
			for _, r := range wlist(out, "reviews") {
				rows = append(rows, []string{wstr(r, "pull"), wstr(r, "agent"), wstr(r, "verdict", "decision"), wstr(r, "counts_as_approval"), wstr(r, "verdict", "summary")})
			}
			w.cl.wtable("PULL\tAGENT\tDECISION\tCOUNTS\tSUMMARY", rows)
		}}, nil
	}
}

// ---- autonomy and samples ----

func (cl *client) workAutonomy(args []string) error {
	if args[0] != "clear-pause" {
		return usageErr("unknown command work autonomy %s\n\n%s", args[0], workUsage)
	}
	return cl.workDo("autonomy clear-pause", args[1:], func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		reason := w.fs.String("reason", "", "why agents may work again; it is recorded")
		return func([]string) (*workReq, error) {
			sc, err := w.needScope()
			if err != nil {
				return nil, err
			}
			if *reason == "" {
				return nil, usageErr("launchway work autonomy clear-pause --reason TEXT [--scope S]")
			}
			return &workReq{method: "POST", path: "/api/v1/work/autonomy/clear-pause", body: map[string]any{"scope": sc, "reason": *reason},
				show: func(any) { fmt.Fprintf(w.cl.out, "Agents may work at %s again.\n", sc) }}, nil
		}
	})
}

func (cl *client) workSamples(args []string) error {
	return cl.workDo("samples "+args[0], args[1:], func(w *wcmd) func([]string) (*workReq, error) {
		all := w.fs.Bool("all", false, "include the ones already checked")
		note := w.fs.String("note", "", "what you found")
		return func(pos []string) (*workReq, error) {
			switch args[0] {
			case "list":
				a := ""
				if *all {
					a = "true"
				}
				return &workReq{method: "GET", path: "/api/v1/work/samples" + q("all", a), show: func(out any) {
					rows := [][]string{}
					for _, s := range wlist(out, "samples") {
						rows = append(rows, []string{wstr(s, "id"), wstr(s, "agent"), wstr(s, "repo"), wstr(s, "pull"), wstr(s, "flagged_at"), wstr(s, "reviewed_by")})
					}
					w.cl.wtable("ID\tAGENT\tREPO\tPULL\tFLAGGED\tCHECKED BY", rows)
				}}, nil
			case "complete":
				if len(pos) != 1 {
					return nil, usageErr("launchway work samples complete ID [--note TEXT]")
				}
				return &workReq{method: "POST", path: "/api/v1/work/samples/" + urlPath(pos[0]) + "/complete", body: map[string]any{"note": *note},
					show: func(any) { fmt.Fprintln(w.cl.out, "Recorded.") }}, nil
			}
			return nil, usageErr("unknown command work samples %s\n\n%s", args[0], workUsage)
		}
	})
}

// ---- what an agent's own scripts call ----

func (cl *client) workAgent(args []string) error {
	verb := args[0]
	return cl.workDo("agent "+verb, args[1:], func(w *wcmd) func([]string) (*workReq, error) {
		const g = "/api/v1/work/agent"
		limit := w.fs.String("limit", "", "how many")
		item := w.fs.String("item", "", "a particular item (default: the next in your list)")
		lease := w.fs.Int("lease", 0, "seconds the claim lasts without a heartbeat")
		note := w.fs.String("note", "", "a note")
		reason := w.fs.String("reason", "", "why")
		summary := w.fs.String("summary", "", "what you did and how to check it")
		tokens := w.fs.Int64("tokens", 0, "tokens spent")
		cents := w.fs.Int64("cents", 0, "cents spent")
		seconds := w.fs.Int64("seconds", 0, "seconds spent")
		repo := w.fs.String("repo", "", "OWNER/NAME of the repository")
		pull := w.fs.Int("pull", 0, "the pull request's number")
		verdict := w.fs.String("verdict-file", "", "a JSON file with the verdict")
		return func(pos []string) (*workReq, error) {
			ref := ""
			if len(pos) == 1 {
				ref = urlPath(pos[0])
			}
			needRef := func(what string) error {
				if ref == "" {
					return usageErr("launchway work agent %s REF %s", verb, what)
				}
				return nil
			}
			switch verb {
			case "me":
				return &workReq{method: "GET", path: g + "/me"}, nil
			case "queue":
				return &workReq{method: "GET", path: g + "/queue" + q("limit", *limit), show: func(out any) { w.cl.itemRows(wlist(out, "items")) }}, nil
			case "claim":
				return &workReq{method: "POST", path: g + "/claim", body: map[string]any{"item": *item, "lease_seconds": *lease, "note": *note},
					show: func(out any) { fmt.Fprintf(w.cl.out, "You hold %s: %s\n", wstr(out, "ref"), wstr(out, "title")) }}, nil
			case "heartbeat":
				return &workReq{method: "POST", path: g + "/items/" + ref + "/heartbeat", body: map[string]any{"note": *note, "lease_seconds": *lease}}, needRef("[--note T]")
			case "hand-back":
				if *reason == "" {
					return nil, usageErr("launchway work agent hand-back REF --reason TEXT")
				}
				return &workReq{method: "POST", path: g + "/items/" + ref + "/hand-back", body: map[string]any{"reason": *reason}}, needRef("--reason TEXT")
			case "request-review":
				if *summary == "" {
					return nil, usageErr("launchway work agent request-review REF --summary TEXT")
				}
				return &workReq{method: "POST", path: g + "/items/" + ref + "/request-review", body: map[string]any{"summary": *summary}}, needRef("--summary TEXT")
			case "usage":
				return &workReq{method: "POST", path: g + "/items/" + ref + "/usage", body: map[string]any{"tokens": *tokens, "cents": *cents, "seconds": *seconds, "note": *note}}, needRef("[--tokens N] [--cents N] [--seconds N]")
			case "pull-review":
				return pullReview(g, *repo, *pull, *verdict)
			}
			return nil, usageErr("unknown command work agent %s\n\n%s", verb, workUsage)
		}
	})
}

// pullReview sends an agent's verdict, read from a JSON file.
func pullReview(g, repo string, pull int, file string) (*workReq, error) {
	if repo == "" || pull <= 0 || file == "" {
		return nil, usageErr("launchway work agent pull-review --repo OWNER/NAME --pull N --verdict-file FILE")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, usageErr("%s is not a JSON verdict: %v", file, err)
	}
	return &workReq{method: "POST", path: g + "/pull-review", body: map[string]any{"repo": repo, "pull": pull, "verdict": v}}, nil
}

// scopeObject is a scope written project:owner/name as the API's {kind, id}
// (a bare owner/name is a project).
func scopeObject(sc string) map[string]string {
	kind, id, ok := strings.Cut(sc, ":")
	if !ok {
		if strings.Contains(sc, "/") {
			return map[string]string{"kind": "project", "id": sc}
		}
		kind, id = sc, ""
	}
	o := map[string]string{"kind": kind}
	if id != "" {
		o["id"] = id
	}
	return o
}
