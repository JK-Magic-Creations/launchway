package main

import (
	"fmt"
	"strings"
)

// launchway work decisions ...: decision records, which are work items of type decision.

func (cl *client) workDecisions(args []string) error {
	name, rest := args[0], args[1:]
	switch name {
	case "list":
		return cl.workDo("decisions list", rest, decisionsList)
	case "show":
		return cl.workDo("decisions show", rest, decisionsShow)
	case "accept":
		return cl.workDo("decisions accept", rest, decisionsAccept)
	case "supersede":
		return cl.workDo("decisions supersede", rest, decisionsSupersede)
	case "export":
		return cl.workDo("decisions export", rest, decisionsExport)
	}
	return usageErr("unknown command work decisions %s\n\n%s", name, workUsage)
}

func decisionsList(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	status := w.fs.String("status", "", "proposed, accepted or superseded")
	inRepo := w.fs.String("in-repo", "", "only the decisions of this repository (OWNER/NAME)")
	return func([]string) (*workReq, error) {
		return &workReq{method: "GET", path: "/api/v1/work/decisions" + q("scope", w.scopeValue(), "repository", *inRepo, "status", *status), show: func(out any) {
			rows := [][]string{}
			for _, d := range wlist(out, "items") {
				rows = append(rows, []string{wstr(d, "item"), wstr(d, "status"), wstr(d, "decided_by"), wstr(d, "title")})
			}
			w.cl.wtable("ITEM\tSTATUS\tDECIDED BY\tTITLE", rows)
		}}, nil
	}
}

func decisionsShow(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work decisions show REF")
		}
		return &workReq{method: "GET", path: apiItem(pos[0]) + "/decision", show: func(out any) {
			fmt.Fprintf(w.cl.out, "%s  %s\nStatus: %s\n", wstr(out, "item"), wstr(out, "title"), wstr(out, "status"))
			if a := wstr(out, "answer"); a != "" {
				fmt.Fprintf(w.cl.out, "Decided by %s on %s: %s\n", wstr(out, "decided_by"), wstr(out, "decided_at"), a)
			}
			if by := wstr(out, "superseded_by"); by != "" {
				fmt.Fprintf(w.cl.out, "Replaced by %s\n", by)
			}
			var older []string
			for _, o := range wlist(out, "supersedes") {
				older = append(older, fmt.Sprint(o))
			}
			if len(older) > 0 {
				fmt.Fprintf(w.cl.out, "Replaces %s\n", strings.Join(older, ", "))
			}
			for _, k := range []struct{ key, label string }{{"decides", "Decides"}, {"blocks", "Blocks"}} {
				for _, i := range wlist(out, k.key) {
					fmt.Fprintf(w.cl.out, "%s %s  %s\n", k.label, wstr(i, "ref"), wstr(i, "title"))
				}
			}
			for _, p := range wlist(out, "pulls") {
				fmt.Fprintf(w.cl.out, "Pull request %s#%s  %s\n", wstr(p, "repo"), wstr(p, "number"), wstr(p, "title"))
			}
		}}, nil
	}
}

func decisionsAccept(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 2 {
			return nil, usageErr("launchway work decisions accept REF ANSWER")
		}
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/decision/accept", body: map[string]any{"answer": pos[1]}, show: func(out any) {
			fmt.Fprintf(w.cl.out, "%s is accepted.\n", wstr(out, "item"))
		}}, nil
	}
}

func decisionsSupersede(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 2 {
			return nil, usageErr("launchway work decisions supersede REF BY_REF")
		}
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/decision/supersede", body: map[string]any{"by": pos[1]}, show: func(out any) {
			fmt.Fprintf(w.cl.out, "%s is replaced by %s.\n", wstr(out, "item"), wstr(out, "superseded_by"))
		}}, nil
	}
}

func decisionsExport(w *wcmd) func([]string) (*workReq, error) {
	folder := w.fs.String("folder", "", "the folder in the repository (default docs/decisions)")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work decisions export OWNER/NAME [--folder DIR]")
		}
		base, err := repoPath(pos[0])
		if err != nil {
			return nil, err
		}
		b := map[string]any{}
		setIf(b, "folder", *folder)
		return &workReq{method: "POST", path: base + "/decisions/export", body: b, show: func(out any) {
			if wstr(out, "changed") == "0" {
				fmt.Fprintln(w.cl.out, wstr(out, "message"))
				return
			}
			fmt.Fprintf(w.cl.out, "Wrote %s file(s) on %s and opened pull request #%s.\n", wstr(out, "changed"), wstr(out, "branch"), wstr(out, "pull"))
			if e := wstr(out, "pull_error"); e != "" {
				fmt.Fprintln(w.cl.out, e)
			}
		}}, nil
	}
}
