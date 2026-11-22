package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const inboxUsage = `Usage:
  launchway inbox list [--state unread|open|done|all] [--kind KIND] [--source notice|work|pull] [--json]
                                   your one inbox: notices, work entries, and the pull requests that wait on you
  launchway inbox count [--json]   how many are unread and how many wait on you
  launchway inbox read ID... [--unread]   mark entries read (or unread again)
  launchway inbox done ID... [--reopen]   mark entries done (or open again)
`

const noticesUsage = `Usage:
  launchway notices prefs [--json]                 what you are told of, how, and your watches, quiet hours and digest
  launchway notices set KIND in_app|email|off      choose how one kind of notice reaches you
  launchway notices watch SCOPE all|participating|none
                                                   your level for repo:owner/name or flight:NAME
  launchway notices quiet --from 22:00 --to 07:00 [--zone Europe/Berlin]   hold mail during these hours
  launchway notices quiet --off                    no quiet hours
  launchway notices digest on|off [--hour 8]       one mail a day instead of one per notice
  launchway notices mail [--json]                  administrators: the mail settings and the outbox
  launchway notices mail-test [--to ADDRESS]       administrators: send one test message and say how it went

Mail is sent only when an administrator sets the mail section of the installation settings.
`

func cmdInbox(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, inboxUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.inboxList(args[1:])
		case "count":
			return cl.inboxCount(args[1:])
		case "read":
			return cl.inboxMark("read", args[1:])
		case "done":
			return cl.inboxMark("done", args[1:])
		}
		return usageErr("unknown command inbox %s\n\n%s", args[0], inboxUsage)
	})
}

type inboxView struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Repository string `json:"repository"`
	At         string `json:"at"`
	Read       bool   `json:"read"`
	Done       bool   `json:"done"`
	Waiting    bool   `json:"waiting_on_you"`
}

func (cl *client) inboxList(args []string) error {
	fs := flag.NewFlagSet("inbox list", flag.ContinueOnError)
	state := fs.String("state", "", "unread, open (the default), done or all")
	kind := fs.String("kind", "", "only one kind of notice")
	source := fs.String("source", "", "notice, work or pull")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q := url.Values{"state": {*state}, "kind": {*kind}, "source": {*source}}
	var out struct {
		Items   []inboxView `json:"items"`
		Unread  int         `json:"unread"`
		Waiting int         `json:"waiting"`
	}
	if err := cl.call("GET", "/api/v1/inbox?"+q.Encode(), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Items) == 0 {
			fmt.Fprintln(cl.out, "Nothing here.")
		}
		for _, it := range out.Items {
			mark := " "
			switch {
			case it.Waiting:
				mark = "!"
			case !it.Read:
				mark = "*"
			}
			fmt.Fprintf(cl.out, "%s %s  %s  %s\n", mark, it.ID, it.At, it.Title)
		}
		fmt.Fprintf(cl.out, "%d unread, %d waiting on you\n", out.Unread, out.Waiting)
	})
}

func (cl *client) inboxCount(args []string) error {
	fs := flag.NewFlagSet("inbox count", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var out struct {
		Unread  int `json:"unread"`
		Waiting int `json:"waiting"`
	}
	if err := cl.call("GET", "/api/v1/inbox/count", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "%d unread, %d waiting on you\n", out.Unread, out.Waiting) })
}

func (cl *client) inboxMark(what string, args []string) error {
	fs := flag.NewFlagSet("inbox "+what, flag.ContinueOnError)
	undo := fs.Bool("unread", false, "mark unread again")
	if what == "done" {
		undo = fs.Bool("reopen", false, "open again")
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageErr("launchway inbox %s ID...", what)
	}
	var out struct {
		Changed int `json:"changed"`
	}
	if err := cl.call("PUT", "/api/v1/inbox/"+what, map[string]any{"ids": pos, "value": !*undo}, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%d changed\n", out.Changed)
	return nil
}

func cmdNotices(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, noticesUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "prefs":
			return cl.noticesPrefs(args[1:])
		case "set":
			if len(args) != 3 {
				return usageErr("launchway notices set KIND in_app|email|off")
			}
			return cl.noticesPut(map[string]any{"kinds": map[string]string{args[1]: args[2]}})
		case "watch":
			if len(args) != 3 {
				return usageErr("launchway notices watch SCOPE all|participating|none")
			}
			return cl.noticesPut(map[string]any{"watches": []map[string]string{{"scope": args[1], "level": args[2]}}})
		case "quiet":
			return cl.noticesQuiet(args[1:])
		case "digest":
			return cl.noticesDigest(args[1:])
		case "mail":
			return cl.noticesMail(args[1:])
		case "mail-test":
			return cl.noticesMailTest(args[1:])
		}
		return usageErr("unknown command notices %s\n\n%s", args[0], noticesUsage)
	})
}

type prefsView struct {
	Kinds []struct {
		Kind    string `json:"kind"`
		Title   string `json:"title"`
		Channel string `json:"channel"`
	} `json:"kinds"`
	Watches []struct {
		Scope string `json:"scope"`
		Level string `json:"level"`
	} `json:"watches"`
	Settings struct {
		QuietStart int    `json:"quiet_start"`
		QuietEnd   int    `json:"quiet_end"`
		Zone       string `json:"zone"`
		Digest     bool   `json:"digest"`
		DigestHour int    `json:"digest_hour"`
	} `json:"settings"`
	MailOn  bool `json:"mail_on"`
	Address bool `json:"address_known"`
}

func clockOf(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func (cl *client) noticesPrefs(args []string) error {
	fs := flag.NewFlagSet("notices prefs", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var p prefsView
	if err := cl.call("GET", "/api/v1/notices/preferences", nil, &p); err != nil {
		return err
	}
	return cl.print(p, *asJSON, func() { cl.printPrefs(p) })
}

func (cl *client) printPrefs(p prefsView) {
	for _, k := range p.Kinds {
		fmt.Fprintf(cl.out, "%-20s %-7s %s\n", k.Kind, k.Channel, k.Title)
	}
	for _, w := range p.Watches {
		fmt.Fprintf(cl.out, "watch %s: %s\n", w.Scope, w.Level)
	}
	if p.Settings.QuietStart >= 0 {
		fmt.Fprintf(cl.out, "quiet hours %s to %s (%s)\n", clockOf(p.Settings.QuietStart), clockOf(p.Settings.QuietEnd), p.Settings.Zone)
	}
	if p.Settings.Digest {
		fmt.Fprintf(cl.out, "digest daily at %02d:00 (%s)\n", p.Settings.DigestHour, p.Settings.Zone)
	}
	switch {
	case !p.MailOn:
		fmt.Fprintln(cl.out, "This installation sends no mail; everything stays in your inbox.")
	case !p.Address:
		fmt.Fprintln(cl.out, "No address is known for you: sign in once with an address in your profile.")
	}
}

func (cl *client) noticesPut(body map[string]any) error {
	var p prefsView
	if err := cl.call("PUT", "/api/v1/notices/preferences", body, &p); err != nil {
		return err
	}
	cl.printPrefs(p)
	return nil
}

func parseClock(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, e1 := strconv.Atoi(h)
	mm, e2 := strconv.Atoi(m)
	if !ok || e1 != nil || e2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, usageErr("%q is not a time such as 22:00", s)
	}
	return hh*60 + mm, nil
}

func (cl *client) currentSettings() (map[string]any, error) {
	var p prefsView
	if err := cl.call("GET", "/api/v1/notices/preferences", nil, &p); err != nil {
		return nil, err
	}
	s := p.Settings
	return map[string]any{"quiet_start": s.QuietStart, "quiet_end": s.QuietEnd, "zone": s.Zone, "digest": s.Digest, "digest_hour": s.DigestHour}, nil
}

func (cl *client) noticesQuiet(args []string) error {
	fs := flag.NewFlagSet("notices quiet", flag.ContinueOnError)
	from := fs.String("from", "", "when quiet hours start, such as 22:00")
	to := fs.String("to", "", "when they end, such as 07:00")
	zone := fs.String("zone", "", "your time zone, such as Europe/Berlin")
	off := fs.Bool("off", false, "no quiet hours")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	s, err := cl.currentSettings()
	if err != nil {
		return err
	}
	if *off {
		s["quiet_start"], s["quiet_end"] = -1, -1
	} else {
		if *from == "" || *to == "" {
			return usageErr("launchway notices quiet --from 22:00 --to 07:00, or --off")
		}
		a, err := parseClock(*from)
		if err != nil {
			return err
		}
		b, err := parseClock(*to)
		if err != nil {
			return err
		}
		s["quiet_start"], s["quiet_end"] = a, b
	}
	if *zone != "" {
		s["zone"] = *zone
	}
	return cl.noticesPut(map[string]any{"settings": s})
}

func (cl *client) noticesDigest(args []string) error {
	fs := flag.NewFlagSet("notices digest", flag.ContinueOnError)
	hour := fs.Int("hour", -1, "the hour of the day it is sent, 0 to 23")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (pos[0] != "on" && pos[0] != "off") {
		return usageErr("launchway notices digest on|off [--hour 8]")
	}
	s, err := cl.currentSettings()
	if err != nil {
		return err
	}
	s["digest"] = pos[0] == "on"
	if *hour >= 0 {
		s["digest_hour"] = *hour
	}
	return cl.noticesPut(map[string]any{"settings": s})
}

func (cl *client) noticesMail(args []string) error {
	fs := flag.NewFlagSet("notices mail", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var m struct {
		Enabled      bool   `json:"enabled"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
		Security     string `json:"security"`
		From         string `json:"from"`
		Pending      int    `json:"pending"`
		Sent         int    `json:"sent"`
		Failed       int    `json:"failed"`
		SentLastHour int    `json:"sent_last_hour"`
	}
	if err := cl.call("GET", "/api/v1/mail", nil, &m); err != nil {
		return err
	}
	return cl.print(m, *asJSON, func() {
		if !m.Enabled {
			fmt.Fprintln(cl.out, "No mail server is set: the mail section of the installation settings is empty. People have their inbox only.")
			return
		}
		fmt.Fprintf(cl.out, "%s:%d (%s), from %s\n%d waiting, %d sent (%d in the last hour), %d failed\n", m.Host, m.Port, m.Security, m.From, m.Pending, m.Sent, m.SentLastHour, m.Failed)
	})
}

func (cl *client) noticesMailTest(args []string) error {
	fs := flag.NewFlagSet("notices mail-test", flag.ContinueOnError)
	to := fs.String("to", "", "the address (default: your own)")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var out struct {
		Sent  bool   `json:"sent"`
		To    string `json:"to"`
		Error string `json:"error"`
	}
	if err := cl.call("POST", "/api/v1/mail/test", map[string]any{"to": *to}, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Sent a test message to %s.\n", out.To)
	return nil
}
