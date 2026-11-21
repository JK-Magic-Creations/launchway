package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const notificationsUsage = `Usage: launchway notifications [--limit N] [--json]

The log of notifications: what was sent where, what waits for a retry and what
could not be sent, with the reason. Channels' addresses and keys are never shown.
`

func cmdNotifications(args []string) error {
	return runClient(func() error {
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).notifications(args)
	})
}

func (cl *client) notifications(args []string) error {
	fs := flag.NewFlagSet("notifications", flag.ContinueOnError)
	limit := fs.Int("limit", 50, "how many of the newest to list")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		fmt.Print(notificationsUsage)
		return err
	}
	var out []engine.NotificationView
	if err := cl.call("GET", fmt.Sprintf("/api/v1/notifications?limit=%d", *limit), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out) == 0 {
			fmt.Fprintln(cl.out, "No notifications yet.")
			return
		}
		for _, n := range out {
			line := fmt.Sprintf("%s  %-8s %s -> %s: %s", time.UnixMilli(n.CreatedAt).Format("Jan 02 15:04"), n.Status, n.Event, n.Channel, n.Title)
			if n.Status != "sent" && n.LastError != "" {
				line += fmt.Sprintf(" (attempt %d: %s)", n.Attempts, n.LastError)
			}
			fmt.Fprintln(cl.out, line)
		}
	})
}
