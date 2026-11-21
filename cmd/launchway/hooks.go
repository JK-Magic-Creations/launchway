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

// hookCall is one `repos hooks` command: its flags, its target and its arguments.
type hookCall struct {
	cl        *client
	verb      string
	base      string
	pos       []string
	given     map[string]bool
	url, name string
	evs       []string
	secret    *string
	active    bool
	asJSON    bool
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
	c := &hookCall{cl: cl, verb: args[0], base: base, pos: pos, given: visited(fs), url: *hookURL, name: *name, active: *active, asJSON: *asJSON}
	if c.given["secret-file"] {
		raw, err := os.ReadFile(*secretFile) //nolint:gosec // the person named the file of their own secret
		if err != nil {
			return err
		}
		v := strings.TrimSpace(string(raw))
		c.secret = &v
	}
	c.evs = []string{}
	for _, e := range strings.Split(*events, ",") {
		if e = strings.TrimSpace(e); e != "" {
			c.evs = append(c.evs, e)
		}
	}
	return c.run()
}

func (c *hookCall) need(n int) error {
	if len(c.pos) != n {
		return usageErr("launchway repos hooks %s: wrong number of arguments%s", c.verb, hooksUsage)
	}
	return nil
}

func (c *hookCall) item(i int) string { return c.base + "/" + url.PathEscape(c.pos[i]) }

func (c *hookCall) run() error {
	switch c.verb {
	case "list":
		return c.list()
	case "add":
		return c.add()
	case "update":
		return c.update()
	case "delete":
		if err := c.need(2); err != nil {
			return err
		}
		if err := c.cl.call("DELETE", c.item(1), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(c.cl.out, "Deleted %s and its delivery log.\n", c.pos[1])
		return nil
	case "test":
		return c.delivery(2, "POST", c.item(1)+"/test")
	case "redeliver":
		if err := c.need(3); err != nil {
			return err
		}
		return c.delivery(3, "POST", c.item(1)+"/deliveries/"+url.PathEscape(c.pos[2])+"/redeliver")
	case "deliveries":
		return c.deliveries()
	}
	return usageErr("unknown command repos hooks %s", c.verb)
}

func (c *hookCall) list() error {
	var page struct {
		Items []hookView `json:"items"`
	}
	if err := c.cl.call("GET", c.base, nil, &page); err != nil {
		return err
	}
	return c.cl.print(page, c.asJSON, func() {
		if len(page.Items) == 0 {
			fmt.Fprintln(c.cl.out, "No webhooks.")
		}
		for _, h := range page.Items {
			state := "on"
			if !h.Active {
				state = "off"
			}
			fmt.Fprintf(c.cl.out, "%s  %s  %s  [%s]  %s\n", h.ID, h.Name, h.URL, strings.Join(h.Events, ","), state)
			if h.DisabledReason != "" {
				fmt.Fprintf(c.cl.out, "    %s\n", h.DisabledReason)
			}
		}
	})
}

func (c *hookCall) add() error {
	if err := c.need(1); err != nil {
		return err
	}
	body := map[string]any{"name": c.name, "url": c.url, "events": c.evs}
	if c.secret != nil {
		body["secret"] = *c.secret
	}
	var out struct {
		Hook   hookView `json:"hook"`
		Secret string   `json:"secret"`
		Note   string   `json:"secret_note"`
	}
	if err := c.cl.call("POST", c.base, body, &out); err != nil {
		return err
	}
	return c.cl.print(out, c.asJSON, func() {
		fmt.Fprintf(c.cl.out, "Added %s  %s\n", out.Hook.ID, out.Hook.URL)
		if out.Secret != "" {
			fmt.Fprintf(c.cl.out, "Secret (shown once): %s\n%s\n", out.Secret, out.Note)
		}
	})
}

func (c *hookCall) update() error {
	if err := c.need(2); err != nil {
		return err
	}
	body := map[string]any{}
	for flagName, key := range map[string]string{"url": "url", "name": "name"} {
		if c.given[flagName] {
			body[key] = map[string]string{"url": c.url, "name": c.name}[flagName]
		}
	}
	if c.given["events"] {
		body["events"] = c.evs
	}
	if c.given["active"] {
		body["active"] = c.active
	}
	if c.secret != nil {
		body["secret"] = *c.secret
	}
	if len(body) == 0 {
		return usageErr("say what to change: --url, --events, --name, --active or --secret-file")
	}
	var h hookView
	if err := c.cl.call("PATCH", c.item(1), body, &h); err != nil {
		return err
	}
	return c.cl.print(h, c.asJSON, func() { fmt.Fprintf(c.cl.out, "Updated %s  %s  active %v\n", h.ID, h.URL, h.Active) })
}

// delivery makes a call that answers with one delivery (a test, a redelivery) and prints it.
func (c *hookCall) delivery(args int, method, path string) error {
	if err := c.need(args); err != nil {
		return err
	}
	var d deliveryView
	if err := c.cl.call(method, path, map[string]any{}, &d); err != nil {
		return err
	}
	return c.cl.print(d, c.asJSON, func() { fmt.Fprintln(c.cl.out, d.line()) })
}

func (c *hookCall) deliveries() error {
	if err := c.need(2); err != nil {
		return err
	}
	var page struct {
		Items []deliveryView `json:"items"`
	}
	if err := c.cl.call("GET", c.item(1)+"/deliveries", nil, &page); err != nil {
		return err
	}
	return c.cl.print(page, c.asJSON, func() {
		if len(page.Items) == 0 {
			fmt.Fprintln(c.cl.out, "No deliveries yet.")
		}
		for _, d := range page.Items {
			fmt.Fprintln(c.cl.out, d.line())
		}
	})
}
