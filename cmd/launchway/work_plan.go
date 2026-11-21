package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// launchway work board, milestones, sprints, charts, roadmap, release notes,
// settings, views, inbox and home.

func (cl *client) workBoard(args []string) error {
	return cl.workDo("board", args, func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		query := w.fs.String("query", "", "narrow the board with a search")
		lanes := w.fs.String("lanes", "", "assignee, label, priority, type or parent")
		sprint := w.fs.String("sprint", "", "only this sprint's items")
		return func([]string) (*workReq, error) {
			path, err := w.withScope("/api/v1/work/board", "q", *query, "lanes", *lanes, "sprint", *sprint)
			if err != nil {
				return nil, err
			}
			return &workReq{method: "GET", path: path, show: func(out any) { w.cl.showBoard(out) }}, nil
		}
	})
}

// showBoard prints each column with its items.
func (cl *client) showBoard(b any) {
	for _, col := range wlist(b, "columns") {
		fmt.Fprintf(cl.out, "== %s (%d)\n", nonEmptyStr(wstr(col, "name"), wstr(col, "state")), len(wlist(col, "items")))
		for _, it := range wlist(col, "items") {
			fmt.Fprintf(cl.out, "  %-10s %-8s %s  %s\n", wstr(it, "ref"), wstr(it, "priority"), wnames(wlist(it, "assignees")), wstr(it, "title"))
		}
	}
	if len(wlist(b, "columns")) == 0 {
		cl.out.Write([]byte("Nothing on the board.\n"))
	}
}

func (cl *client) workRoadmap(args []string) error {
	return cl.workDo("roadmap", args, func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		return func([]string) (*workReq, error) {
			path, err := w.withScope("/api/v1/work/roadmap")
			if err != nil {
				return nil, err
			}
			return &workReq{method: "GET", path: path, show: func(out any) {
				rows := [][]string{}
				for _, r := range wlist(out, "rows") {
					rows = append(rows, []string{wstr(r, "ref"), wstr(r, "kind"), wstr(r, "start_at"), wstr(r, "due_at"), wstr(r, "percent"), wstr(r, "title")})
				}
				w.cl.wtable("REF\tKIND\tSTART\tDUE\tPERCENT\tTITLE", rows)
			}}, nil
		}
	})
}

func (cl *client) workReleaseNotes(args []string) error {
	return cl.workDo("release-notes", args, func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		since := w.fs.String("since", "", "from this date")
		until := w.fs.String("until", "", "up to this date")
		return func([]string) (*workReq, error) {
			path, err := w.withScope("/api/v1/work/release-notes", "since", *since, "until", *until)
			if err != nil {
				return nil, err
			}
			return &workReq{method: "GET", path: path}, nil
		}
	})
}

func (cl *client) workHome(args []string) error {
	return cl.workDo("home", args, func(w *wcmd) func([]string) (*workReq, error) {
		return func([]string) (*workReq, error) {
			return &workReq{method: "GET", path: "/api/v1/work/home", show: func(out any) {
				fmt.Fprintf(w.cl.out, "Assigned to you (%s):\n", wstr(out, "assigned_total"))
				w.cl.itemRows(wlist(out, "assigned"))
				fmt.Fprintf(w.cl.out, "\nUnread in your inbox: %s\n", wstr(out, "unread"))
				if s := wlist(out, "samples"); len(s) > 0 {
					fmt.Fprintf(w.cl.out, "Agent changes waiting for you to check: %d\n", len(s))
				}
			}}, nil
		}
	})
}

func (cl *client) workInbox(args []string) error {
	return cl.workDo("inbox", args, func(w *wcmd) func([]string) (*workReq, error) {
		unread := w.fs.Bool("unread", false, "only what you have not read")
		read := w.fs.Bool("read", false, "mark what is shown as read")
		return func([]string) (*workReq, error) {
			u := ""
			if *unread {
				u = "true"
			}
			var ids []any
			return &workReq{method: "GET", path: "/api/v1/work/inbox" + q("unread", u),
				enrich: func(out any) {
					for _, e := range wlist(out, "entries") {
						ids = append(ids, wget(e, "id"))
					}
				},
				show: func(out any) {
					rows := [][]string{}
					for _, e := range wlist(out, "entries") {
						rows = append(rows, []string{wstr(e, "at"), wstr(e, "kind"), wstr(e, "ref"), wstr(e, "text")})
					}
					w.cl.wtable("WHEN\tKIND\tITEM\tTEXT", rows)
					fmt.Fprintf(w.cl.out, "\n%s unread.\n", wstr(out, "unread"))
				},
				then: func() error {
					if !*read || len(ids) == 0 {
						return nil
					}
					return w.cl.call("POST", "/api/v1/work/inbox/read", map[string]any{"ids": ids}, nil)
				}}, nil
		}
	})
}

func (cl *client) workSpend(args []string) error {
	return cl.workDo("spend", args, func(w *wcmd) func([]string) (*workReq, error) {
		return func([]string) (*workReq, error) {
			return &workReq{method: "GET", path: "/api/v1/work/spend", show: func(out any) {
				rows := [][]string{}
				for _, r := range wlist(out, "rows") {
					rows = append(rows, []string{wstr(r, "name"), wstr(r, "kind"), wstr(r, "spent_tokens"), wstr(r, "spent_cents"), wstr(r, "seconds"), wstr(r, "period")})
				}
				w.cl.wtable("NAME\tKIND\tTOKENS\tCENTS\tSECONDS\tPERIOD", rows)
			}}, nil
		}
	})
}

// ---- milestones ----

func (cl *client) workMilestones(args []string) error {
	switch args[0] {
	case "list":
		return cl.workDo("milestones list", args[1:], msList)
	case "create":
		return cl.workDo("milestones create", args[1:], msCreate)
	case "update":
		return cl.workDo("milestones update", args[1:], msUpdate)
	}
	return usageErr("unknown command work milestones %s\n\n%s", args[0], workUsage)
}

func msList(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	return func([]string) (*workReq, error) {
		path, err := w.withScope("/api/v1/work/milestones")
		if err != nil {
			return nil, err
		}
		return &workReq{method: "GET", path: path, show: func(out any) {
			rows := [][]string{}
			for _, m := range wlist(out, "milestones") {
				rows = append(rows, []string{wstr(m, "id"), wstr(m, "title"), wstr(m, "state"), wstr(m, "due_at"), wstr(m, "done") + "/" + wstr(m, "total"), wstr(m, "percent") + "%"})
			}
			w.cl.wtable("ID\tTITLE\tSTATE\tDUE\tDONE\tPERCENT", rows)
		}}, nil
	}
}

func msCreate(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	desc := w.fs.String("description", "", "what it is for")
	due := w.fs.String("due", "", "due date")
	return func(pos []string) (*workReq, error) {
		sc, err := w.needScope()
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 {
			return nil, usageErr("launchway work milestones create TITLE [--scope S] [--description T] [--due DATE]")
		}
		b := map[string]any{"scope": sc, "title": pos[0]}
		if *desc != "" {
			b["description"] = *desc
		}
		if *due != "" {
			b["due_at"] = *due
		}
		return &workReq{method: "POST", path: "/api/v1/work/milestones", body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Made milestone %s (%s).\n", wstr(out, "title"), wstr(out, "id"))
		}}, nil
	}
}

func msUpdate(w *wcmd) func([]string) (*workReq, error) {
	title := w.fs.String("title", "", "a new title")
	desc := w.fs.String("description", "", "what it is for")
	due := w.fs.String("due", "", "due date")
	state := w.fs.String("state", "", "open or closed")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work milestones update ID [--title T] [--description T] [--due DATE] [--state open|closed]")
		}
		g := w.given()
		b := map[string]any{}
		for n, p := range map[string]*string{"title": title, "description": desc, "state": state} {
			if g[n] {
				b[n] = *p
			}
		}
		if g["due"] {
			b["due_at"] = *due
		}
		return &workReq{method: "PATCH", path: "/api/v1/work/milestones/" + urlPath(pos[0]), body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Changed milestone %s [%s].\n", wstr(out, "title"), wstr(out, "state"))
		}}, nil
	}
}

// ---- sprints ----

func (cl *client) workSprints(args []string) error {
	switch args[0] {
	case "list":
		return cl.workDo("sprints list", args[1:], spList)
	case "create":
		return cl.workDo("sprints create", args[1:], spCreate)
	case "update":
		return cl.workDo("sprints update", args[1:], spUpdate)
	case "show", "burndown", "start", "complete":
		return cl.workDo("sprints "+args[0], args[1:], spByID(args[0]))
	}
	return usageErr("unknown command work sprints %s\n\n%s", args[0], workUsage)
}

func spList(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	return func([]string) (*workReq, error) {
		path, err := w.withScope("/api/v1/work/sprints")
		if err != nil {
			return nil, err
		}
		return &workReq{method: "GET", path: path, show: func(out any) {
			rows := [][]string{}
			for _, s := range wlist(out, "sprints") {
				rows = append(rows, []string{wstr(s, "id"), wstr(s, "name"), wstr(s, "state"), wstr(s, "start_at"), wstr(s, "end_at"), wstr(s, "completed") + "/" + wstr(s, "committed")})
			}
			w.cl.wtable("ID\tNAME\tSTATE\tSTART\tEND\tDONE/COMMITTED", rows)
		}}, nil
	}
}

// sprintID is a sprint's id: what was given if it is one, otherwise the id of
// the sprint of that name at the scope.
func (w *wcmd) sprintID(arg string) (string, error) {
	if strings.HasPrefix(arg, "spr_") || w.scopeValue() == "" {
		return arg, nil
	}
	var out any
	path, _ := w.withScope("/api/v1/work/sprints")
	if err := w.cl.call("GET", path, nil, &out); err != nil {
		return "", err
	}
	for _, s := range wlist(out, "sprints") {
		if wstr(s, "name") == arg || wstr(s, "id") == arg {
			return wstr(s, "id"), nil
		}
	}
	return arg, nil
}

func spCreate(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	goal := w.fs.String("goal", "", "what the sprint is for")
	start := w.fs.String("start", "", "start date")
	end := w.fs.String("end", "", "end date")
	var capacity multi
	w.fs.Var(&capacity, "capacity", "PERSON=N: what a person can take on (repeat for more)")
	return func(pos []string) (*workReq, error) {
		sc, err := w.needScope()
		if err != nil {
			return nil, err
		}
		if len(pos) != 1 || *start == "" || *end == "" {
			return nil, usageErr("launchway work sprints create NAME --start DATE --end DATE [--goal T] [--capacity PERSON=N ...]")
		}
		b := map[string]any{"scope": sc, "name": pos[0], "start_at": *start, "end_at": *end}
		if *goal != "" {
			b["goal"] = *goal
		}
		if err := addCapacity(b, capacity); err != nil {
			return nil, err
		}
		return &workReq{method: "POST", path: "/api/v1/work/sprints", body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Planned sprint %s (%s).\n", wstr(out, "name"), wstr(out, "id"))
		}}, nil
	}
}

func addCapacity(b map[string]any, capacity multi) error {
	if len(capacity) == 0 {
		return nil
	}
	m := map[string]float64{}
	for _, c := range capacity {
		name, n, ok := strings.Cut(c, "=")
		f, err := strconv.ParseFloat(n, 64)
		if !ok || err != nil || name == "" {
			return usageErr("--capacity is PERSON=N, such as ada=20")
		}
		m[name] = f
	}
	b["capacity"] = m
	return nil
}

func spUpdate(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	name := w.fs.String("name", "", "a new name")
	goal := w.fs.String("goal", "", "what the sprint is for")
	start := w.fs.String("start", "", "start date")
	end := w.fs.String("end", "", "end date")
	var capacity multi
	w.fs.Var(&capacity, "capacity", "PERSON=N (repeat for more)")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work sprints update ID [--name N] [--goal T] [--start DATE] [--end DATE] [--capacity PERSON=N ...]")
		}
		id, err := w.sprintID(pos[0])
		if err != nil {
			return nil, err
		}
		g := w.given()
		b := map[string]any{}
		for n, p := range map[string]*string{"name": name, "goal": goal} {
			if g[n] {
				b[n] = *p
			}
		}
		if g["start"] {
			b["start_at"] = *start
		}
		if g["end"] {
			b["end_at"] = *end
		}
		if err := addCapacity(b, capacity); err != nil {
			return nil, err
		}
		return &workReq{method: "PATCH", path: "/api/v1/work/sprints/" + urlPath(id), body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Changed sprint %s.\n", wstr(out, "name"))
		}}, nil
	}
}

// spByID is show, burndown, start and complete: one sprint, one route.
func spByID(verb string) func(w *wcmd) func([]string) (*workReq, error) {
	return func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		carry := new(string)
		if verb == "complete" {
			carry = w.fs.String("carry-to", "", "the sprint that takes the unfinished items (default: the backlog)")
		}
		return func(pos []string) (*workReq, error) {
			if len(pos) != 1 {
				return nil, usageErr("launchway work sprints %s ID", verb)
			}
			id, err := w.sprintID(pos[0])
			if err != nil {
				return nil, err
			}
			base := "/api/v1/work/sprints/" + urlPath(id)
			switch verb {
			case "show":
				return &workReq{method: "GET", path: base, show: func(out any) {
					fmt.Fprintf(w.cl.out, "%s [%s] %s to %s\n", wstr(out, "name"), wstr(out, "state"), wstr(out, "start_at"), wstr(out, "end_at"))
					w.cl.wkv(out, "goal", "committed", "completed", "added", "carried_over", "items", "items_done", "total_capacity", "warning")
				}}, nil
			case "burndown":
				return &workReq{method: "GET", path: base + "/burndown"}, nil
			case "start":
				return &workReq{method: "POST", path: base + "/start", show: func(out any) { fmt.Fprintf(w.cl.out, "Started sprint %s.\n", wstr(out, "name")) }}, nil
			}
			return &workReq{method: "POST", path: base + "/complete", body: map[string]any{"carry_to": *carry},
				show: func(out any) { fmt.Fprintf(w.cl.out, "Completed sprint %s.\n", wstr(out, "name")) }}, nil
		}
	}
}

// ---- charts, settings, views ----

func (cl *client) workCharts(args []string) error {
	chart := args[0]
	if chart != "velocity" && chart != "flow" && chart != "times" {
		return usageErr("unknown chart %s: velocity, flow or times\n\n%s", chart, workUsage)
	}
	return cl.workDo("charts "+chart, args[1:], func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		sprints := w.fs.String("sprints", "", "for velocity: how many of the latest sprints")
		days := w.fs.String("days", "", "for flow and times: how many days back")
		return func([]string) (*workReq, error) {
			more := []string{"days", *days}
			if chart == "velocity" {
				more = []string{"sprints", *sprints}
			}
			path, err := w.withScope("/api/v1/work/charts/"+chart, more...)
			return &workReq{method: "GET", path: path}, err
		}
	})
}

func (cl *client) workSettings(args []string) error {
	return cl.workDo("settings "+args[0], args[1:], func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		return func(pos []string) (*workReq, error) {
			switch args[0] {
			case "show":
				path, err := w.withScope("/api/v1/work/settings")
				return &workReq{method: "GET", path: path}, err
			case "set":
				return settingsSet(w, pos)
			}
			return nil, usageErr("unknown command work settings %s\n\n%s", args[0], workUsage)
		}
	})
}

func settingsSet(w *wcmd, pos []string) (*workReq, error) {
	sc, err := w.needScope()
	if err != nil {
		return nil, err
	}
	if len(pos) != 1 {
		return nil, usageErr("launchway work settings set FILE [--scope S]")
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return nil, err
	}
	var settings any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, usageErr("%s is not JSON: %v", pos[0], err)
	}
	return &workReq{method: "PUT", path: "/api/v1/work/settings", body: map[string]any{"scope": sc, "settings": settings}, then: func() error {
		fmt.Fprintf(w.cl.out, "Settings of %s replaced.\n", sc)
		return nil
	}}, nil
}

func (cl *client) workViews(args []string) error {
	return cl.workDo("views "+args[0], args[1:], func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		query := w.fs.String("query", "", "the search to save")
		shared := w.fs.Bool("shared", false, "offer it to everyone who reads the scope")
		return func(pos []string) (*workReq, error) {
			switch args[0] {
			case "list":
				path, err := w.withScope("/api/v1/work/views")
				return &workReq{method: "GET", path: path, show: func(out any) {
					rows := [][]string{}
					for _, v := range wlist(out, "views") {
						rows = append(rows, []string{wstr(v, "id"), wstr(v, "name"), wstr(v, "owner"), wstr(v, "shared"), wstr(v, "query")})
					}
					w.cl.wtable("ID\tNAME\tOWNER\tSHARED\tQUERY", rows)
				}}, err
			case "save":
				sc, err := w.needScope()
				if err != nil || len(pos) != 1 || *query == "" {
					return nil, nonNil(err, usageErr("launchway work views save NAME --query Q [--shared] [--scope S]"))
				}
				return &workReq{method: "POST", path: "/api/v1/work/views", body: map[string]any{"scope": sc, "name": pos[0], "query": *query, "shared": *shared},
					show: func(out any) { fmt.Fprintf(w.cl.out, "Saved %s (%s).\n", wstr(out, "name"), wstr(out, "id")) }}, nil
			case "delete":
				if len(pos) != 1 {
					return nil, usageErr("launchway work views delete ID")
				}
				return &workReq{method: "DELETE", path: "/api/v1/work/views/" + urlPath(pos[0]), show: func(any) { fmt.Fprintln(w.cl.out, "View deleted.") }}, nil
			}
			return nil, usageErr("unknown command work views %s\n\n%s", args[0], workUsage)
		}
	})
}

func nonNil(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
