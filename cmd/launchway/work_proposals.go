package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// launchway work propose | proposals | health: analyzers send findings, the
// server groups and caps them, and a person reviews the groups.

// workStdin is where `--from -` reads.
var workStdin io.Reader = os.Stdin

func (cl *client) workPropose(args []string) error {
	return cl.workDo("propose", args, func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		from := w.fs.String("from", "", "a JSON file: {source, run_id, report_url, complete, proposals: [...]}; - reads standard input")
		return func(pos []string) (*workReq, error) {
			sc, err := w.needScope()
			if err != nil {
				return nil, err
			}
			if *from == "" || len(pos) != 0 {
				return nil, usageErr("launchway work propose --from FILE [--scope S | --repo OWNER/NAME]")
			}
			body, err := readBatch(*from, sc)
			if err != nil {
				return nil, err
			}
			return &workReq{method: "POST", path: "/api/v1/work/proposals", body: body, show: func(out any) { w.cl.showPropose(out) }}, nil
		}
	})
}

// readBatch reads a batch file and adds the scope to it.
func readBatch(from, scope string) (map[string]any, error) {
	var raw []byte
	var err error
	if from == "-" {
		raw, err = io.ReadAll(io.LimitReader(workStdin, 32<<20))
	} else {
		raw, err = os.ReadFile(from)
	}
	if err != nil {
		return nil, err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, usageErr("%s is not a JSON object with source and proposals: %v", from, err)
	}
	if s, _ := doc["source"].(string); s == "" {
		return nil, usageErr("%s names no source", from)
	}
	doc["scope"] = scope
	return doc, nil
}

func (cl *client) showPropose(out any) {
	fmt.Fprintf(cl.out, "Received %s findings: %s new, %s known, %s fixed, %s left out as dismissed.\n",
		wstr(out, "received"), wstr(out, "new"), wstr(out, "updated"), wstr(out, "fixed"), wstr(out, "dismissed"))
	fmt.Fprintf(cl.out, "%s groups waiting for a review; %s items opened, %s updated, %s closed, %s reopened.\n",
		wstr(out, "waiting"), wstr(out, "opened"), wstr(out, "synced"), wstr(out, "closed"), wstr(out, "reopened"))
}

func (cl *client) workProposals(args []string) error {
	verb := args[0]
	return cl.workDo("proposals "+verb, args[1:], func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		state := w.fs.String("state", "", "list: only groups that are pending, accepted, folded, dismissed or fixed")
		source := w.fs.String("source", "", "the analyzer the groups came from")
		reason := w.fs.String("reason", "", "dismiss: why")
		into := w.fs.String("into", "", "merge: the group the others join")
		var titles multi
		w.fs.Var(&titles, "title-for", "accept: GROUP=TEXT, a different title for the item of a group (repeatable)")
		return func(pos []string) (*workReq, error) {
			switch verb {
			case "list":
				path, err := w.withScope("/api/v1/work/proposals", "state", *state)
				return &workReq{method: "GET", path: path, show: func(out any) { w.cl.showProposals(out) }}, err
			case "accept", "dismiss", "merge":
				return reviewReq(w, verb, *source, pos, *reason, *into, titles)
			}
			return nil, usageErr("unknown command work proposals %s\n\n%s", verb, workUsage)
		}
	})
}

func reviewReq(w *wcmd, verb, source string, groups []string, reason, into string, titles multi) (*workReq, error) {
	sc, err := w.needScope()
	if err != nil {
		return nil, err
	}
	if source == "" || len(groups) == 0 {
		return nil, usageErr("launchway work proposals %s --source S GROUP... (see launchway work proposals list)", verb)
	}
	body := map[string]any{"scope": sc, "source": source, "groups": groups}
	switch verb {
	case "dismiss":
		if strings.TrimSpace(reason) == "" {
			return nil, usageErr("say why: launchway work proposals dismiss --source S GROUP... --reason TEXT")
		}
		body["reason"] = reason
	case "merge":
		if into == "" {
			return nil, usageErr("say where: launchway work proposals merge --source S GROUP... --into GROUP")
		}
		body["into"] = into
	default:
		edits, err := titleEdits(titles)
		if err != nil {
			return nil, err
		}
		body["edits"] = edits
	}
	return &workReq{method: "POST", path: "/api/v1/work/proposals/" + verb, body: body, show: func(out any) { w.cl.showReview(verb, out) }}, nil
}

// titleEdits reads the --title-for GROUP=TEXT flags.
func titleEdits(titles multi) (map[string]any, error) {
	edits := map[string]any{}
	for _, t := range titles {
		g, text, ok := strings.Cut(t, "=")
		if !ok || g == "" || text == "" {
			return nil, usageErr("--title-for takes GROUP=TEXT, not %q", t)
		}
		edits[g] = map[string]any{"title": text}
	}
	return edits, nil
}

func (cl *client) showReview(verb string, out any) {
	switch verb {
	case "accept":
		fmt.Fprintf(cl.out, "Accepted: %s items opened, %s updated.\n", wstr(out, "opened"), wstr(out, "synced"))
	case "dismiss":
		fmt.Fprintf(cl.out, "Dismissed %s findings; they will not be proposed again.\n", wstr(out, "dismissed"))
	default:
		fmt.Fprintln(cl.out, "Merged.")
	}
}

func (cl *client) showProposals(out any) {
	rows := [][]string{}
	for _, g := range wlist(out, "groups") {
		rows = append(rows, []string{wstr(g, "source"), wstr(g, "group"), wstr(g, "state"), wstr(g, "severity"), wstr(g, "unresolved") + "/" + wstr(g, "count"), wstr(g, "item"), wstr(g, "title")})
	}
	cl.wtable("SOURCE\tGROUP\tSTATE\tSEVERITY\tOPEN\tITEM\tTITLE", rows)
}

func (cl *client) workHealth(args []string) error {
	return cl.workDo("health", args, func(w *wcmd) func([]string) (*workReq, error) {
		repo := w.fs.String("repo", "", "OWNER/NAME of the hosted repository to check")
		return func(pos []string) (*workReq, error) {
			if *repo == "" || len(pos) != 0 {
				return nil, usageErr("launchway work health --repo OWNER/NAME")
			}
			return &workReq{method: "POST", path: "/api/v1/work/health", body: map[string]any{"repository": *repo}, show: func(out any) { w.cl.showPropose(out) }}, nil
		}
	})
}
