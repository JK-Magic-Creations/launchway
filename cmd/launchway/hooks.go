package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const hooksUsage = `
  launchway repos hooks list TARGET [--json]              the webhooks of a repository (TARGET is OWNER/NAME) or of an owner (TARGET is OWNER)
  launchway repos hooks add TARGET --url URL --events push,create,delete,repository
                            [--name N] [--secret-file F]   send events to an address, signed with HMAC-SHA256 (a secret is made and shown once if none is given)
  launchway repos hooks update TARGET ID [--url U] [--events E] [--name N] [--active=false] [--secret-file F]
                                                          change one; --active turns it on again after it was turned off
  launchway repos hooks delete TARGET ID                  delete a webhook and its delivery log
  launchway repos hooks test TARGET ID                    send a test delivery now and show what the address answered
  launchway repos hooks deliveries TARGET ID [--json]     the delivery log: request, status, time taken, attempts
  launchway repos hooks redeliver TARGET ID DELIVERY      send a delivery's request again
`

type hookView struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	Active         bool     `json:"active"`
	Failures       int      `json:"failures"`
	DisabledReason string   `json:"disabled_reason"`
}

type deliveryView struct {
	ID         string `json:"id"`
	Event      string `json:"event"`
	State      string `json:"state"`
	Attempts   int    `json:"attempts"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error"`
	CreatedAt  string `json:"created_at"`
}

func (d deliveryView) line() string {
	s := fmt.Sprintf("%s  %s  %s  status %d  %d ms  %d attempt(s)  %s", d.ID, d.Event, d.State, d.Status, d.DurationMS, d.Attempts, d.CreatedAt)
	if d.Error != "" {
		s += "\n    " + d.Error
	}
	return s
}

// hooksBase is the route of a target's hooks: OWNER/NAME for a repository, OWNER for an owner.
func hooksBase(target string) (string, error) {
	if owner, name, ok := splitRepo(target); ok {
		return "/api/v1/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/hooks", nil
	}
	if target == "" || strings.Contains(target, "/") {
		return "", usageErr("the target is OWNER/NAME (a repository) or OWNER (every repository of an owner), not %q", target)
	}
	return "/api/v1/owners/" + url.PathEscape(target) + "/hooks", nil
}

func (cl *client) hooks(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway repos hooks list|add|update|delete|test|deliveries|redeliver TARGET ...%s", hooksUsage)
	}
	fs := flag.NewFlagSet("repos hooks "+args[0], flag.ContinueOnError)
	hookURL := fs.String("url", "", "the address that receives the deliveries")
	events := fs.String("events", "", "comma separated: push, create, delete, repository")
	name := fs.String("name", "", "a name for the webhook")
	secretFile := fs.String("secret-file", "", "a file holding the secret that signs each delivery (never on the command line)")
	active := fs.Bool("active", true, "on or off; --active=false turns it off")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usageErr("launchway repos hooks %s TARGET ...", args[0])
	}
	base, err := hooksBase(pos[0])
	if err != nil {
		return err
	}
	given := visited(fs)
	var secret *string
	if given["secret-file"] {
		raw, err := os.ReadFile(*secretFile)
		if err != nil {
			return err
		}
		v := strings.TrimSpace(string(raw))
		secret = &v
	}
	evs := []string{}
	for _, e := range strings.Split(*events, ",") {
		if e = strings.TrimSpace(e); e != "" {
			evs = append(evs, e)
		}
	}
	need := func(n int) error {
		if len(pos) != n {
			return usageErr("launchway repos hooks %s: wrong number of arguments%s", args[0], hooksUsage)
		}
		return nil
	}
	switch args[0] {
	case "list":
		var page struct {
			Items []hookView `json:"items"`
		}
		if err := cl.call("GET", base, nil, &page); err != nil {
			return err
		}
		return cl.print(page, *asJSON, func() {
			if len(page.Items) == 0 {
				fmt.Fprintln(cl.out, "No webhooks.")
			}
			for _, h := range page.Items {
				state := "on"
				if !h.Active {
					state = "off"
				}
				fmt.Fprintf(cl.out, "%s  %s  %s  [%s]  %s\n", h.ID, h.Name, h.URL, strings.Join(h.Events, ","), state)
				if h.DisabledReason != "" {
					fmt.Fprintf(cl.out, "    %s\n", h.DisabledReason)
				}
			}
		})
	case "add":
		if err := need(1); err != nil {
			return err
		}
		body := map[string]any{"name": *name, "url": *hookURL, "events": evs}
		if secret != nil {
			body["secret"] = *secret
		}
		var out struct {
			Hook   hookView `json:"hook"`
			Secret string   `json:"secret"`
			Note   string   `json:"secret_note"`
		}
		if err := cl.call("POST", base, body, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			fmt.Fprintf(cl.out, "Added %s  %s\n", out.Hook.ID, out.Hook.URL)
			if out.Secret != "" {
				fmt.Fprintf(cl.out, "Secret (shown once): %s\n%s\n", out.Secret, out.Note)
			}
		})
	case "update":
		if err := need(2); err != nil {
			return err
		}
		body := map[string]any{}
		if given["url"] {
			body["url"] = *hookURL
		}
		if given["events"] {
			body["events"] = evs
		}
		if given["name"] {
			body["name"] = *name
		}
		if given["active"] {
			body["active"] = *active
		}
		if secret != nil {
			body["secret"] = *secret
		}
		if len(body) == 0 {
			return usageErr("say what to change: --url, --events, --name, --active or --secret-file")
		}
		var h hookView
		if err := cl.call("PATCH", base+"/"+url.PathEscape(pos[1]), body, &h); err != nil {
			return err
		}
		return cl.print(h, *asJSON, func() { fmt.Fprintf(cl.out, "Updated %s  %s  active %v\n", h.ID, h.URL, h.Active) })
	case "delete":
		if err := need(2); err != nil {
			return err
		}
		if err := cl.call("DELETE", base+"/"+url.PathEscape(pos[1]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Deleted %s and its delivery log.\n", pos[1])
		return nil
	case "test":
		if err := need(2); err != nil {
			return err
		}
		var d deliveryView
		if err := cl.call("POST", base+"/"+url.PathEscape(pos[1])+"/test", map[string]any{}, &d); err != nil {
			return err
		}
		return cl.print(d, *asJSON, func() { fmt.Fprintln(cl.out, d.line()) })
	case "deliveries":
		if err := need(2); err != nil {
			return err
		}
		var page struct {
			Items []deliveryView `json:"items"`
		}
		if err := cl.call("GET", base+"/"+url.PathEscape(pos[1])+"/deliveries", nil, &page); err != nil {
			return err
		}
		return cl.print(page, *asJSON, func() {
			if len(page.Items) == 0 {
				fmt.Fprintln(cl.out, "No deliveries yet.")
			}
			for _, d := range page.Items {
				fmt.Fprintln(cl.out, d.line())
			}
		})
	case "redeliver":
		if err := need(3); err != nil {
			return err
		}
		var d deliveryView
		if err := cl.call("POST", base+"/"+url.PathEscape(pos[1])+"/deliveries/"+url.PathEscape(pos[2])+"/redeliver", map[string]any{}, &d); err != nil {
			return err
		}
		return cl.print(d, *asJSON, func() { fmt.Fprintln(cl.out, d.line()) })
	}
	return usageErr("unknown command repos hooks %s", args[0])
}
