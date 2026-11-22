package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// launchway work import: stories from a file, or a repository's archived issues.

func (cl *client) workImport(args []string) error {
	return cl.workDo("import", args, func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		format := w.fs.String("format", "", "stories-json or archive")
		repository := w.fs.String("repository", "", "for archive: OWNER/NAME of the repository whose archived issues become items")
		preview := w.fs.Bool("preview", false, "say what would be made without making it")
		update := w.fs.Bool("update", false, "for stories: change the items a key already made")
		return func(pos []string) (*workReq, error) {
			switch *format {
			case "stories-json":
				return importStories(w, pos, *preview, *update)
			case "archive":
				if *repository == "" || len(pos) != 0 {
					return nil, usageErr("launchway work import --format archive --repository OWNER/NAME [--preview]")
				}
				return &workReq{method: "POST", path: "/api/v1/work/import/archive", body: map[string]any{"repository": *repository, "preview": *preview},
					show: func(out any) { w.cl.showImport(out, *preview) }}, nil
			}
			return nil, usageErr("say the format: launchway work import --format stories-json FILE | --format archive --repository OWNER/NAME")
		}
	})
}

// importStories reads a file of stories: a JSON list, or an object with items.
func importStories(w *wcmd, pos []string, preview, update bool) (*workReq, error) {
	sc, err := w.needScope()
	if err != nil {
		return nil, err
	}
	if len(pos) != 1 {
		return nil, usageErr("launchway work import --format stories-json FILE [--scope S] [--preview] [--update]")
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return nil, err
	}
	var items json.RawMessage
	var doc struct {
		Items json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &doc) == nil && len(doc.Items) > 0 {
		items = doc.Items
	} else {
		items = raw
	}
	var check []any
	if err := json.Unmarshal(items, &check); err != nil {
		return nil, usageErr("%s is not a list of stories (a JSON array, or an object with items): %v", pos[0], err)
	}
	return &workReq{method: "POST", path: "/api/v1/work/import/stories", body: map[string]any{"scope": sc, "items": items, "preview": preview, "update": update},
		show: func(out any) { w.cl.showImport(out, preview) }}, nil
}

func (cl *client) showImport(out any, preview bool) {
	verb := "Made"
	if preview {
		verb = "Would make"
	}
	fmt.Fprintf(cl.out, "%s %s items; %s already known", verb, wstr(out, "created"), wstr(out, "existing"))
	if u := wstr(out, "updated"); u != "" && u != "0" {
		fmt.Fprintf(cl.out, "; %s changed", u)
	}
	fmt.Fprintln(cl.out, ".")
	for _, p := range wlist(out, "problems") {
		fmt.Fprintf(cl.out, "  problem: %v\n", p)
	}
}
