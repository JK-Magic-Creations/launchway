package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const cachesUsage = `Usage:
  launchway caches list PROJECT [--json]              a project's build caches, and the quota they count against
  launchway caches delete PROJECT --name N | --id ID  end one cache entry, or every entry of a cache
  launchway caches policy get [PROJECT] [--json]      whether caches are kept, and how much
  launchway caches policy set [PROJECT]               change the policy (administrators)
                     [--enabled=false] [--max-project-gib N] [--max-entry-gib N] [--idle-days N] [--max-age-days N]
  launchway caches policy clear PROJECT               drop a project's own policy (administrators)

Listing needs the rights to read the project, deleting those to operate it, and the
policy is an administrator's. Deleting is recorded in the audit trail; the next run that
wanted the entry starts without it and saves it again. Without a project, policy get and
set are the installation's; with one, the project's own policy (an override of the
installation's), and policy clear removes it.
`

func cmdCaches(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, cachesUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.cachesList(args[1:])
		case "delete":
			return cl.cachesDelete(args[1:])
		case "policy":
			return cl.cachesPolicy(args[1:])
		}
		return usageErr("unknown command caches %s\n\n%s", args[0], cachesUsage)
	})
}

func byteText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func (cl *client) cachesList(args []string) error {
	fs := flag.NewFlagSet("caches list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway caches list PROJECT [--json]")
	}
	var l engine.CacheList
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/caches", nil, &l); err != nil {
		return err
	}
	return cl.print(l, *asJSON, func() {
		if !l.Policy.Enabled {
			fmt.Fprintln(cl.out, "Caching is turned off for this project (cache policy).")
		}
		if len(l.Entries) == 0 {
			fmt.Fprintln(cl.out, "Nothing is cached.")
		}
		for _, v := range l.Entries {
			used := "never restored"
			if v.Hits > 0 {
				used = fmt.Sprintf("restored %d times, last %s", v.Hits, time.UnixMilli(v.LastUsedAt).UTC().Format("2006-01-02 15:04"))
			}
			state := ""
			if v.Status != "kept" {
				state = " (" + v.Status + ")"
			}
			key := v.Key
			if len(key) > 12 {
				key = key[:12]
			}
			fmt.Fprintf(cl.out, "%s  %-12s %s  %-22s %-12s %10s%s  %s, run #%d\n", v.ID, v.Name, key, v.Scope, v.Platform, byteText(v.Bytes), state, used, v.RunNumber)
		}
		fmt.Fprintf(cl.out, "%d entries, %s of %s; an entry may hold %s and is kept %d days without being restored\n",
			l.Totals.Entries, byteText(l.Totals.Bytes), byteText(l.Totals.QuotaBytes), byteText(l.Totals.MaxEntryBytes), l.Totals.IdleDays)
	})
}

func (cl *client) cachesDelete(args []string) error {
	fs := flag.NewFlagSet("caches delete", flag.ContinueOnError)
	name := fs.String("name", "", "end every entry of this cache, in every scope and key")
	id := fs.String("id", "", "end this entry")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*name == "") == (*id == "") {
		return usageErr("launchway caches delete PROJECT --name N | --id ID")
	}
	path := "/api/v1/projects/" + url.PathEscape(pos[0]) + "/caches"
	if *id != "" {
		path += "/" + url.PathEscape(*id)
	} else {
		path += "?name=" + url.QueryEscape(*name)
	}
	var out engine.CachesDeleted
	if err := cl.call("DELETE", path, nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Ended %d cache entries, %s. The next run that wanted them starts without them.\n", out.Ended, byteText(out.Bytes))
	})
}

func policyPath(project string) string {
	if project == "" {
		return "/api/v1/cache-policy"
	}
	return "/api/v1/projects/" + url.PathEscape(project) + "/cache-policy"
}

func (cl *client) cachesPolicy(args []string) error {
	if len(args) == 0 {
		return usageErr("%s", cachesUsage)
	}
	switch args[0] {
	case "get":
		return cl.cachesPolicyGet(args[1:])
	case "set":
		return cl.cachesPolicySet(args[1:])
	case "clear":
		return cl.cachesPolicyClear(args[1:])
	}
	return usageErr("unknown command caches policy %s\n\n%s", args[0], cachesUsage)
}

func printPolicy(cl *client, v engine.CachePolicyView) {
	if !v.Enabled {
		fmt.Fprintf(cl.out, "Caching is OFF (%s). ", v.Source)
	} else {
		fmt.Fprintf(cl.out, "Caching is on (%s). ", v.Source)
	}
	fmt.Fprintf(cl.out, "A project keeps at most %g GiB, an entry at most %g GiB, and an entry nobody restores is ended after %d days; a restore of one older than %d days counts as a miss and the step rebuilds it.\n",
		v.MaxProjectGiB, v.MaxEntryGiB, v.IdleDays, v.MaxAgeDays)
}

func (cl *client) cachesPolicyGet(args []string) error {
	fs := flag.NewFlagSet("caches policy get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageErr("launchway caches policy get [PROJECT] [--json]")
	}
	project := ""
	if len(pos) == 1 {
		project = pos[0]
	}
	var v engine.CachePolicyView
	if err := cl.call("GET", policyPath(project), nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() { printPolicy(cl, v) })
}

func (cl *client) cachesPolicySet(args []string) error {
	fs := flag.NewFlagSet("caches policy set", flag.ContinueOnError)
	enabled := fs.Bool("enabled", true, "whether caches are kept at all")
	maxProject := fs.Float64("max-project-gib", 0, "how much one project's entries may add up to, in GiB")
	maxEntry := fs.Float64("max-entry-gib", 0, "the most one entry may hold, in GiB")
	idle := fs.Int("idle-days", 0, "how long an entry nobody restored is kept")
	maxAge := fs.Int("max-age-days", 0, "how old an entry may be before a restore treats it as missing and the step rebuilds it")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	given := visited(fs)
	if len(pos) > 1 || (!given["enabled"] && !given["max-project-gib"] && !given["max-entry-gib"] && !given["idle-days"] && !given["max-age-days"]) {
		return usageErr("launchway caches policy set [PROJECT] [--enabled=false] [--max-project-gib N] [--max-entry-gib N] [--idle-days N] [--max-age-days N]")
	}
	body := map[string]any{}
	if given["enabled"] {
		body["enabled"] = *enabled
	}
	if given["max-project-gib"] {
		body["max_project_gib"] = *maxProject
	}
	if given["max-entry-gib"] {
		body["max_entry_gib"] = *maxEntry
	}
	if given["idle-days"] {
		body["idle_days"] = *idle
	}
	if given["max-age-days"] {
		body["max_age_days"] = *maxAge
	}
	project := ""
	if len(pos) == 1 {
		project = pos[0]
	}
	var v engine.CachePolicyView
	if err := cl.call("PUT", policyPath(project), body, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() { printPolicy(cl, v) })
}

func (cl *client) cachesPolicyClear(args []string) error {
	fs := flag.NewFlagSet("caches policy clear", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway caches policy clear PROJECT")
	}
	var v engine.CachePolicyView
	if err := cl.call("DELETE", policyPath(pos[0]), nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() { printPolicy(cl, v) })
}
