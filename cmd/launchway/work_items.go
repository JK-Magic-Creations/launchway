package main

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// launchway work items ...

func (cl *client) workItems(args []string) error {
	name, rest := args[0], args[1:]
	switch name {
	case "list":
		return cl.workDo("items list", rest, itemsList)
	case "show":
		return cl.workDo("items show", rest, itemsShow)
	case "create":
		return cl.workDo("items create", rest, itemsCreate)
	case "update":
		return cl.workDo("items update", rest, itemsUpdate)
	case "move":
		return cl.workDo("items move", rest, itemsMove)
	case "rank":
		return cl.workDo("items rank", rest, itemsRank)
	case "comment":
		return cl.workDo("items comment", rest, itemsComment)
	case "link":
		return cl.workDo("items link", rest, itemsLink)
	case "unlink":
		return cl.workDo("items unlink", rest, itemsUnlink)
	case "code-link":
		return cl.workDo("items code-link", rest, itemsCodeLink)
	case "by-code":
		return cl.workDo("items by-code", rest, itemsByCode)
	case "history":
		return cl.workDo("items history", rest, itemsHistory)
	case "take-over":
		return cl.workDo("items take-over", rest, itemsTakeOver)
	case "react":
		return cl.workDo("items react", rest, itemsReact)
	case "attach":
		return cl.workAttach(rest)
	case "file":
		return cl.workFile(rest)
	}
	return usageErr("unknown command work items %s\n\n%s", name, workUsage)
}

// itemRows is the table of items.
func (cl *client) itemRows(items []any) {
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, []string{wstr(it, "ref"), wstr(it, "type"), wstr(it, "state"), wstr(it, "priority"), wnames(wlist(it, "assignees")), wstr(it, "title")})
	}
	cl.wtable("REF\tTYPE\tSTATE\tPRIORITY\tASSIGNEES\tTITLE", rows)
}

func itemsList(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	query := w.fs.String("query", "", "the search: words, state:open, assignee:me, label:x")
	sort := w.fs.String("sort", "", "rank, updated, created, priority, due or number")
	desc := w.fs.Bool("desc", false, "largest first")
	limit := w.fs.String("limit", "", "how many (at most 200)")
	offset := w.fs.String("offset", "", "how many to skip")
	parent := w.fs.String("parent", "", "only the children of this item")
	inRepo := w.fs.String("in-repo", "", "only the work of this repository (OWNER/NAME): its own and the work tied to its code")
	return func([]string) (*workReq, error) {
		d := ""
		if *desc {
			d = "true"
		}
		sc := w.scopeValue()
		return &workReq{method: "GET", path: "/api/v1/work/items" + q("scope", sc, "q", *query, "sort", *sort, "desc", d, "limit", *limit, "offset", *offset, "parent", *parent, "repository", *inRepo),
			show: func(out any) {
				w.cl.itemRows(wlist(out, "items"))
				fmt.Fprintf(w.cl.out, "\n%s of %s items.\n", fmt.Sprint(len(wlist(out, "items"))), wstr(out, "total"))
			}}, nil
	}
}

func itemsShow(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work items show REF")
		}
		ref := pos[0]
		return &workReq{method: "GET", path: apiItem(ref), show: func(out any) { w.cl.showItem(out) },
			enrich: func(out any) {
				var cm any
				if m, ok := out.(map[string]any); ok && w.cl.call("GET", apiItem(ref)+"/comments", nil, &cm) == nil {
					m["comments"] = wget(cm, "comments")
				}
			}}, nil
	}
}

func apiItem(ref string) string { return "/api/v1/work/items/" + urlPath(ref) }

func urlPath(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "/", "%2F"), " ", "%20")
}

// showItem prints one item with what hangs on it.
func (cl *client) showItem(it any) {
	fmt.Fprintf(cl.out, "%s  %s\n", wstr(it, "ref"), wstr(it, "title"))
	cl.wkv(it, "type", "state", "resolution", "priority", "estimate", "difficulty", "parent_ref", "milestone", "sprint", "start_at", "due_at", "reporter", "created_at", "updated_at", "version")
	if l := wnames(wlist(it, "labels")); l != "" {
		fmt.Fprintf(cl.out, "%-12s %s\n", "labels:", l)
	}
	if a := wnames(wlist(it, "assignees")); a != "" {
		fmt.Fprintf(cl.out, "%-12s %s\n", "assignees:", a)
	}
	if c := wget(it, "claim"); c != nil {
		fmt.Fprintf(cl.out, "%-12s agent %s until %s\n", "held by:", wstr(c, "agent"), wstr(c, "lease_until"))
	}
	if b := wstr(it, "blocked"); b == "true" {
		fmt.Fprintf(cl.out, "%-12s %s\n", "blocked by:", wnames(wlist(it, "blocked_by")))
	}
	if body := wstr(it, "body"); body != "" {
		fmt.Fprintf(cl.out, "\n%s\n", body)
	}
	for _, c := range wlist(it, "criteria") {
		mark := " "
		if wstr(c, "done") == "true" {
			mark = "x"
		}
		fmt.Fprintf(cl.out, "  [%s] %s\n", mark, wstr(c, "text"))
	}
	cl.showItemLinks(it)
}

func (cl *client) showItemLinks(it any) {
	for _, l := range wlist(it, "links") {
		fmt.Fprintf(cl.out, "link %s: %s %s -> %s %s  (%s)\n", wstr(l, "kind"), wstr(l, "from"), wstr(l, "from_title"), wstr(l, "to"), wstr(l, "to_title"), wstr(l, "id"))
	}
	for _, c := range wlist(it, "code") {
		fmt.Fprintf(cl.out, "code %s: %s %s %s %s\n", wstr(c, "kind"), wstr(c, "repo"), wstr(c, "ref"), wstr(c, "state"), wstr(c, "title"))
	}
	for _, a := range wlist(it, "attachments") {
		fmt.Fprintf(cl.out, "file %s: %s (%s bytes)  (%s)\n", wstr(a, "kind"), wstr(a, "name"), wstr(a, "size"), wstr(a, "id"))
	}
	if d := wget(it, "delivery"); d != nil {
		fmt.Fprintf(cl.out, "delivery: %v\n", d)
	}
	for _, c := range wlist(it, "comments") {
		fmt.Fprintf(cl.out, "\n--- %s, %s  (%s)\n%s\n", wstr(c, "author"), wstr(c, "created_at"), wstr(c, "id"), wstr(c, "body"))
	}
}

func itemsCreate(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	typ := w.fs.String("type", "", "issue, bug, story, task or epic")
	body := w.fs.String("body", "", "the description")
	bodyFile := w.fs.String("body-file", "", "read the description from a file")
	state := w.fs.String("state", "", "the state to start in")
	priority := w.fs.String("priority", "", "urgent, high, normal or low")
	estimate := w.fs.Float64("estimate", 0, "points or hours")
	parent := w.fs.String("parent", "", "the parent item's reference")
	milestone := w.fs.String("milestone", "", "a milestone's title")
	sprint := w.fs.String("sprint", "", "a sprint's name")
	start := w.fs.String("start", "", "start date")
	due := w.fs.String("due", "", "due date")
	diff := w.fs.String("difficulty", "", "small, medium or large")
	conf := w.fs.Bool("confidential", false, "only those who may change work here see it")
	forRepo := w.fs.String("for-repo", "", "the repository (OWNER/NAME) the item is for: its scope when none is given, otherwise a tie to it")
	var labels, assignees, criteria multi
	w.fs.Var(&labels, "label", "a label (repeat for more)")
	w.fs.Var(&assignees, "assignee", "a person, or agent:NAME (repeat for more)")
	w.fs.Var(&criteria, "criterion", "an acceptance criterion (repeat for more)")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work items create TITLE [--scope S]")
		}
		text, err := bodyText(*body, *bodyFile)
		if err != nil {
			return nil, err
		}
		path := "/api/v1/work/items"
		if w.scopeValue() != "" || *forRepo == "" {
			if path, err = w.withScope(path); err != nil {
				return nil, err
			}
		}
		b := map[string]any{"title": pos[0]}
		setIf(b, "repository", *forRepo)
		setIf(b, "type", *typ)
		setIf(b, "body", text)
		setIf(b, "state", *state)
		setIf(b, "priority", *priority)
		setIf(b, "parent", *parent)
		setIf(b, "milestone", *milestone)
		setIf(b, "sprint", *sprint)
		setIf(b, "start_at", *start)
		setIf(b, "due_at", *due)
		setIf(b, "difficulty", *diff)
		if *estimate != 0 {
			b["estimate"] = *estimate
		}
		if *conf {
			b["confidential"] = true
		}
		for k, v := range map[string]multi{"labels": labels, "assignees": assignees, "criteria": criteria} {
			if len(v) > 0 {
				b[k] = []string(v)
			}
		}
		return &workReq{method: "POST", path: path, body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Made %s: %s\n", wstr(out, "ref"), wstr(out, "title"))
		}}, nil
	}
}

func setIf(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func itemsUpdate(w *wcmd) func([]string) (*workReq, error) {
	str := map[string]*string{}
	for _, n := range []string{"title", "body", "type", "state", "resolution", "priority", "parent", "milestone", "sprint", "start", "due", "difficulty"} {
		str[n] = w.fs.String(n, "", "set the "+n)
	}
	bodyFile := w.fs.String("body-file", "", "read the description from a file")
	estimate := w.fs.Float64("estimate", 0, "points or hours")
	conf := w.fs.Bool("confidential", false, "whether only those who may change work here see it")
	watch := w.fs.Bool("watch", true, "follow the item (--watch=false stops)")
	version := w.fs.Int("expected-version", 0, "refuse the change if the item's version is not this")
	lists := map[string]*multi{}
	for _, n := range []string{"label", "add-label", "remove-label", "assignee", "add-assignee", "remove-assignee"} {
		m := &multi{}
		lists[n] = m
		w.fs.Var(m, n, n+" (repeat for more)")
	}
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work items update REF [flags]")
		}
		g := w.given()
		b := map[string]any{}
		keys := map[string]string{"start": "start_at", "due": "due_at"}
		for n, p := range str {
			if g[n] {
				k := keys[n]
				if k == "" {
					k = n
				}
				b[k] = *p
			}
		}
		if g["body-file"] {
			text, err := bodyText("", *bodyFile)
			if err != nil {
				return nil, err
			}
			b["body"] = text
		}
		if g["estimate"] {
			b["estimate"] = *estimate
		}
		if g["confidential"] {
			b["confidential"] = *conf
		}
		if g["watch"] {
			b["watch"] = *watch
		}
		if g["expected-version"] {
			b["expected_version"] = *version
		}
		for n, m := range lists {
			if g[n] {
				b[strings.ReplaceAll(n, "-", "_")+"s"] = []string(*m)
			}
		}
		if len(b) == 0 {
			return nil, usageErr("say what to change: launchway work items update REF --title T --state S ...")
		}
		return &workReq{method: "PATCH", path: apiItem(pos[0]), body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Changed %s: %s [%s]\n", wstr(out, "ref"), wstr(out, "title"), wstr(out, "state"))
		}}, nil
	}
}

func itemsMove(w *wcmd) func([]string) (*workReq, error) {
	before := w.fs.String("before", "", "put it above this item in the new state")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 2 {
			return nil, usageErr("launchway work items move REF STATE [--before REF]")
		}
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/move", body: map[string]any{"state": pos[1], "before": *before},
			show: func(out any) { fmt.Fprintf(w.cl.out, "%s is now %s.\n", wstr(out, "ref"), wstr(out, "state")) }}, nil
	}
}

func itemsRank(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) < 2 || len(pos) > 3 {
			return nil, usageErr("launchway work items rank REF top|bottom|before OTHER|after OTHER")
		}
		b := map[string]any{"where": pos[1]}
		if len(pos) == 3 {
			b["other"] = pos[2]
		}
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/rank", body: b,
			show: func(out any) { fmt.Fprintf(w.cl.out, "Moved %s in the order.\n", wstr(out, "ref")) }}, nil
	}
}

func itemsComment(w *wcmd) func([]string) (*workReq, error) {
	file := w.fs.String("file", "", "read the comment from a file")
	edit := w.fs.String("edit", "", "the id of a comment to change")
	return func(pos []string) (*workReq, error) {
		want := 2 // REF TEXT
		if *edit != "" {
			want = 1 // TEXT
		}
		if *file != "" {
			want-- // the text is in the file
		}
		if len(pos) != want {
			return nil, usageErr("launchway work items comment REF TEXT  |  work items comment --edit COMMENT_ID TEXT  (or --file F instead of TEXT)")
		}
		text, ref := "", ""
		if *file != "" {
			t, err := bodyText("", *file)
			if err != nil {
				return nil, err
			}
			text = t
		} else {
			text = pos[len(pos)-1]
		}
		if *edit == "" {
			ref = pos[0]
		}
		if *edit != "" {
			return &workReq{method: "PATCH", path: "/api/v1/work/comments/" + urlPath(*edit), body: map[string]any{"body": text},
				show: func(any) { fmt.Fprintln(w.cl.out, "Comment changed.") }}, nil
		}
		return &workReq{method: "POST", path: apiItem(ref) + "/comments", body: map[string]any{"body": text},
			show: func(out any) {
				fmt.Fprintf(w.cl.out, "Commented on %s (%s).\n", wstr(out, "item"), wstr(out, "id"))
			}}, nil
	}
}

func itemsLink(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 3 {
			return nil, usageErr("launchway work items link REF blocks|relates|duplicates OTHER")
		}
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/links", body: map[string]any{"kind": pos[1], "to": pos[2]},
			show: func(out any) {
				fmt.Fprintf(w.cl.out, "%s %s %s.\n", wstr(out, "from"), wstr(out, "kind"), wstr(out, "to"))
			}}, nil
	}
}

func itemsUnlink(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work items unlink LINK_ID")
		}
		return &workReq{method: "DELETE", path: "/api/v1/work/links/" + urlPath(pos[0]), show: func(any) { fmt.Fprintln(w.cl.out, "Link removed.") }}, nil
	}
}

func itemsCodeLink(w *wcmd) func([]string) (*workReq, error) {
	kind := w.fs.String("kind", "", "commit, branch, pull or run")
	repo := w.fs.String("code-repo", "", "OWNER/NAME of the repository")
	ref := w.fs.String("ref", "", "the commit hash, branch name, pull request number or run id")
	title := w.fs.String("title", "", "a title for it")
	state := w.fs.String("state", "", "open, merged or closed; success or failed")
	closes := w.fs.Bool("closes", false, "finishing it finishes the item")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 || *kind == "" || *ref == "" {
			return nil, usageErr("launchway work items code-link REF --kind commit|branch|pull|run --code-repo OWNER/NAME --ref X")
		}
		b := map[string]any{"kind": *kind, "ref": *ref, "closes": *closes}
		setIf(b, "repo", *repo)
		setIf(b, "title", *title)
		setIf(b, "state", *state)
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/code", body: b,
			show: func(out any) { fmt.Fprintf(w.cl.out, "%s tied to %s.\n", wstr(out, "kind"), pos[0]) }}, nil
	}
}

func itemsByCode(w *wcmd) func([]string) (*workReq, error) {
	kind := w.fs.String("kind", "", "commit, branch, pull or run")
	repo := w.fs.String("code-repo", "", "OWNER/NAME of the repository")
	ref := w.fs.String("ref", "", "the commit hash, branch name, pull request number or run id")
	return func([]string) (*workReq, error) {
		if *kind == "" || *ref == "" {
			return nil, usageErr("launchway work items by-code --kind K --code-repo OWNER/NAME --ref X")
		}
		return &workReq{method: "GET", path: "/api/v1/work/by-code" + q("kind", *kind, "repo", *repo, "ref", *ref),
			show: func(out any) { w.cl.itemRows(wlist(out, "items")) }}, nil
	}
}

func itemsHistory(w *wcmd) func([]string) (*workReq, error) {
	after := w.fs.String("after", "", "only events after this event id")
	limit := w.fs.String("limit", "", "how many")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work items history REF")
		}
		return &workReq{method: "GET", path: apiItem(pos[0]) + "/history" + q("after", *after, "limit", *limit), show: func(out any) {
			for _, e := range wlist(out, "events") {
				fmt.Fprintf(w.cl.out, "%s  %-10s %s (%s, via %s)  %v\n", wstr(e, "at"), wstr(e, "kind"), wstr(e, "actor"), wstr(e, "actor_kind"), wstr(e, "via"), wget(e, "data"))
			}
		}}, nil
	}
}

func itemsTakeOver(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work items take-over REF")
		}
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/take-over", show: func(out any) {
			fmt.Fprintf(w.cl.out, "You now hold %s.\n", wstr(out, "ref"))
		}}, nil
	}
}

// workAttach uploads a file as the request body.
func (cl *client) workAttach(args []string) error {
	return cl.workDo("items attach", args, func(w *wcmd) func([]string) (*workReq, error) {
		name := w.fs.String("name", "", "the file's name on the item (default: the file's name)")
		kind := w.fs.String("kind", "", "what it is: screenshot, log...")
		return func(pos []string) (*workReq, error) {
			if len(pos) != 2 {
				return nil, usageErr("launchway work items attach REF FILE [--name N] [--kind K]")
			}
			data, err := os.ReadFile(pos[1])
			if err != nil {
				return nil, err
			}
			n := *name
			if n == "" {
				n = filepath.Base(pos[1])
			}
			ct := mime.TypeByExtension(filepath.Ext(n))
			if ct == "" {
				ct = http.DetectContentType(data)
			}
			return &workReq{method: "POST", path: apiItem(pos[0]) + "/attachments" + q("name", n, "kind", *kind), body: rawBytes{data, ct},
				show: func(out any) { fmt.Fprintf(w.cl.out, "Attached %s (%s).\n", wstr(out, "name"), wstr(out, "id")) }}, nil
		}
	})
}

// workFile downloads a file of an item.
func (cl *client) workFile(args []string) error {
	return cl.workDo("items file", args, func(w *wcmd) func([]string) (*workReq, error) {
		out := w.fs.String("out", "", "write it to this file (default: standard output)")
		return func(pos []string) (*workReq, error) {
			if len(pos) != 1 {
				return nil, usageErr("launchway work items file ATTACHMENT_ID [--out FILE]")
			}
			data, err := w.cl.raw("/api/v1/work/attachments/" + urlPath(pos[0]) + "?download=1")
			if err != nil {
				return nil, err
			}
			if *out == "" {
				_, err = w.cl.out.Write(data)
				return &workReq{}, err
			}
			return &workReq{}, os.WriteFile(*out, data, 0o600)
		}
	})
}

func itemsReact(w *wcmd) func([]string) (*workReq, error) {
	comment := w.fs.String("comment", "", "react to this comment (its id) instead of the item")
	remove := w.fs.Bool("remove", false, "take your reaction back")
	return func(pos []string) (*workReq, error) {
		reaction, p := "", ""
		switch {
		case *comment != "" && len(pos) == 1:
			reaction, p = pos[0], "/api/v1/work/comments/"+url.PathEscape(*comment)
		case *comment == "" && len(pos) == 2:
			reaction, p = pos[1], apiItem(pos[0])
		default:
			return nil, usageErr("launchway work items react REF REACTION [--remove]   or   work items react --comment ID REACTION [--remove]\nreactions: thumbs_up thumbs_down laugh hooray confused heart rocket eyes")
		}
		method := "PUT"
		if *remove {
			method = "DELETE"
		}
		return &workReq{method: method, path: p + "/reactions/" + url.PathEscape(reaction), show: func(out any) {
			var parts []string
			for _, r := range wlist(out, "reactions") {
				mark := ""
				if b, _ := r.(map[string]any)["mine"].(bool); b {
					mark = "*"
				}
				parts = append(parts, fmt.Sprintf("%s %s%s", wstr(r, "reaction"), wstr(r, "count"), mark))
			}
			fmt.Fprintln(w.cl.out, strings.Join(parts, ", "))
		}}, nil
	}
}
