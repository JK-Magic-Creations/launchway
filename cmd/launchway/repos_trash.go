package main

import (
	"flag"
	"fmt"
	"net/url"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// The repository trash: list, restore, remove for good.

const reposTrashUsage = `  launchway repos trash [--json]                       the deleted repositories you administer: who deleted them, when, when each goes for good
  launchway repos restore ID [--owner OWNER] [--name NAME]
                                                         bring a deleted repository back (give --name when its address was taken since)
  launchway repos purge ID --confirm OWNER/NAME          remove a deleted repository for good (administrators; the address typed to confirm; cannot be undone)
`

func (cl *client) reposTrash(args []string) error {
	fs := flag.NewFlagSet("repos trash", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var page forge.Page[forge.TrashedRepository]
	if err := cl.call("GET", "/api/v1/repository-trash", nil, &page); err != nil {
		return err
	}
	return cl.print(page, *asJSON, func() {
		if len(page.Items) == 0 {
			fmt.Fprintln(cl.out, "No deleted repositories.")
			return
		}
		for _, t := range page.Items {
			by := t.DeletedBy
			if by == "" {
				by = "someone"
			}
			note := ""
			switch {
			case t.Purging:
				note = " (being removed)"
			case t.NameTaken:
				note = " (its address was taken: restore it with --name)"
			}
			fmt.Fprintf(cl.out, "%s  %s  deleted by %s on %s, goes for good on %s%s\n", t.ID, t.Path, by,
				t.DeletedAt.Local().Format(time.DateOnly), t.PurgeAt.Local().Format(time.DateOnly), note)
		}
	})
}

func (cl *client) reposRestore(args []string) error {
	fs := flag.NewFlagSet("repos restore", flag.ContinueOnError)
	owner := fs.String("owner", "", "restore under another owner (administrators of everything)")
	name := fs.String("name", "", "restore under another name")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos restore ID [--owner OWNER] [--name NAME]")
	}
	var r forge.Repository
	if err := cl.call("POST", "/api/v1/repository-trash/"+url.PathEscape(pos[0])+"/restore", map[string]string{"owner": *owner, "name": *name}, &r); err != nil {
		return err
	}
	return cl.print(r, *asJSON, func() { fmt.Fprintf(cl.out, "Restored %s.\n", r.Path) })
}

func (cl *client) reposPurge(args []string) error {
	fs := flag.NewFlagSet("repos purge", flag.ContinueOnError)
	confirm := fs.String("confirm", "", "the address the repository had, typed exactly (OWNER/NAME)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos purge ID --confirm OWNER/NAME")
	}
	var sum forge.PurgeSummary
	if err := cl.call("DELETE", "/api/v1/repository-trash/"+url.PathEscape(pos[0]), map[string]string{"confirm": *confirm}, &sum); err != nil {
		return err
	}
	return cl.print(sum, *asJSON, func() {
		fmt.Fprintf(cl.out, "Removed %s for good: %d packs, %d blobs deleted (%d bytes), %d kept because another repository names them.\n",
			sum.Path, sum.Packs, sum.BlobsDeleted, sum.Bytes, sum.BlobsKept)
	})
}
