package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const eventsUsage = `Usage:
  launchway events list [--project P] [--source S] [--decision D] [--verified yes|no]
                        [--ref R] [--limit N] [--json]    what reached the server, newest first, and what was decided
  launchway events show EVENT [--json]                    one event: its payload as kept and every decision with its reason
  launchway events replay EVENT                           start the run the event asked for again (operators; verified events only)

Sources: push, pull_request, schedule, webhook, event_source, manual, run_result, launch_result.
Decisions: started, held, waiting, filtered, superseded, skipped, duplicate, refused, error.
A replay is a new event that points back to the first, at the same commit and with the same inputs.
It is recorded in the Logbook with your name.
`

func cmdEvents(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, eventsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.eventsList(args[1:])
		case "show":
			return cl.eventsShow(args[1:])
		case "replay":
			return cl.eventsReplay(args[1:])
		}
		return usageErr("unknown command events %s\n\n%s", args[0], eventsUsage)
	})
}

func eventTime(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05Z") }

func (cl *client) eventsList(args []string) error {
	fs := flag.NewFlagSet("events list", flag.ContinueOnError)
	project := fs.String("project", "", "only this Flight")
	src := fs.String("source", "", "only this kind of source")
	dec := fs.String("decision", "", "only events with a decision of this kind")
	ver := fs.String("verified", "", "yes or no")
	ref := fs.String("ref", "", "only this ref")
	limit := fs.Int("limit", 0, "how many (default 100)")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range map[string]string{"project": *project, "source": *src, "decision": *dec, "verified": *ver, "ref": *ref} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	var evs []engine.EventRecord
	if err := cl.call("GET", "/api/v1/events?"+q.Encode(), nil, &evs); err != nil {
		return err
	}
	return cl.print(evs, *asJSON, func() {
		if len(evs) == 0 {
			fmt.Fprintln(cl.out, "No events match.")
		}
		for _, ev := range evs {
			mark := ""
			if !ev.Verified {
				mark = " [not verified]"
			}
			fmt.Fprintf(cl.out, "%s  %s  %s  %s%s\n", ev.ID, eventTime(ev.At), ev.Project, ev.Summary, mark)
			for _, d := range ev.Decisions {
				fmt.Fprintf(cl.out, "    %s: %s: %s\n", d.Trigger, d.Decision, d.Reason)
			}
		}
	})
}

func (cl *client) eventsShow(args []string) error {
	fs := flag.NewFlagSet("events show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which event: launchway events show EVENT\n\n%s", eventsUsage)
	}
	var ev engine.EventRecord
	if err := cl.call("GET", "/api/v1/events/"+url.PathEscape(pos[0]), nil, &ev); err != nil {
		return err
	}
	return cl.print(ev, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s\n  Flight  %s\n  source   %s %s\n  at       %s\n  verified %v\n", ev.Summary, ev.Project, ev.Source, ev.SourceName, eventTime(ev.At), ev.Verified)
		if ev.ReplayOf != "" {
			fmt.Fprintf(cl.out, "  replay of %s\n", ev.ReplayOf)
		}
		if len(ev.Payload) > 0 {
			fmt.Fprintf(cl.out, "  payload  %s\n", ev.Payload)
		}
		fmt.Fprintln(cl.out, "Decisions:")
		if len(ev.Decisions) == 0 {
			fmt.Fprintln(cl.out, "  none")
		}
		for _, d := range ev.Decisions {
			run := ""
			if d.RunNumber > 0 {
				run = fmt.Sprintf(" (run #%d)", d.RunNumber)
			}
			fmt.Fprintf(cl.out, "  %s: %s%s: %s\n", d.Trigger, d.Decision, run, d.Reason)
		}
	})
}

func (cl *client) eventsReplay(args []string) error {
	fs := flag.NewFlagSet("events replay", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which event: launchway events replay EVENT\n\n%s", eventsUsage)
	}
	var out struct {
		Run engine.RunSummary `json:"run"`
	}
	if err := cl.call("POST", "/api/v1/events/"+url.PathEscape(pos[0])+"/replay", map[string]string{}, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Replayed %s: run #%d of %s started (%s).\n", pos[0], out.Run.Number, out.Run.Project, out.Run.ID)
	return nil
}
