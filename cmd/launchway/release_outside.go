package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
)

// printOutside lists how a release stands on each push mirror's outside Git host.
func printOutside(w io.Writer, v any) {
	list, _ := v.([]any)
	if len(list) == 0 {
		return
	}
	fmt.Fprintln(w, "  on an outside Git host:")
	for _, x := range list {
		m, _ := x.(map[string]any)
		line := fmt.Sprintf("    %s (%s): %s", m["mirror_url"], m["mirror_id"], m["state"])
		if u, _ := m["url"].(string); u != "" {
			line += " " + u
		}
		if r, _ := m["reason"].(string); r != "" {
			line += " (" + r + ")"
		}
		fmt.Fprintln(w, line)
	}
}

func (cl *client) releaseOutside(args []string) error {
	if len(args) == 0 || args[0] != "retry" {
		return usageErr("launchway release outside retry FLIGHT VERSION --mirror mir_...\n\n%s", releaseUsage)
	}
	fs := flag.NewFlagSet("release outside retry", flag.ContinueOnError)
	mirror := fs.String("mirror", "", "the push mirror (mir_..., from launchway repos mirror status)")
	asJSON := fs.Bool("json", false, "print JSON")
	p, v, err := cl.two("outside retry", args[1:], fs)
	if err != nil {
		return err
	}
	if *mirror == "" {
		return usageErr("say which push mirror: --mirror mir_... (see launchway repos mirror status)")
	}
	var out map[string]any
	if err := cl.call("POST", cl.relPath(p, v, "/outside/"+url.PathEscape(*mirror)+"/retry"), map[string]any{}, &out); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(out)
	}
	fmt.Fprintf(cl.out, "%s %s will be made on the outside Git host again; the server does it within seconds. See launchway release show %s %s.\n", p, v, p, v)
	return nil
}
