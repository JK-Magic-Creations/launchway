package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"text/tabwriter"
)

// reposArchiveUsage is the part of the repos help about the archive of another
// server's issues.
const reposArchiveUsage = `
  launchway repos import-archive OWNER/NAME FILE [--json]
                                    store the issues and comments exported from another server (FILE is the
                                    launchway-archive/1 JSON written by scripts/export-issues.sh) in the
                                    repository's read-only archive; run it again after a fix and the same
                                    numbers are replaced (the repository's administrators)
  launchway repos archive list OWNER/NAME [--state open|closed|all] [--query TEXT] [--page N] [--json]
                                    the imported issues, newest first, 50 to a page
  launchway repos archive show OWNER/NAME NUMBER [--json]
                                    one imported issue with its comments
`

// archiveChunk is how many issues go in one request.
const archiveChunk = 200

type archiveFile struct {
	Format     string            `json:"format"`
	Source     json.RawMessage   `json:"source"`
	ExportedAt string            `json:"exported_at"`
	Issues     []json.RawMessage `json:"issues"`
}

type archiveResult struct {
	Issues   int `json:"issues"`
	Comments int `json:"comments"`
	Replaced int `json:"replaced"`
	Total    int `json:"total"`
}

func (cl *client) reposImportArchive(args []string) error {
	fs := flag.NewFlagSet("repos import-archive", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway repos import-archive OWNER/NAME FILE")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(pos[1])
	if err != nil {
		return err
	}
	var f archiveFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return usageErr("%s is not a launchway-archive/1 document: %v", pos[1], err)
	}
	if len(f.Issues) == 0 {
		return usageErr("%s holds no issues", pos[1])
	}
	var total archiveResult
	for from := 0; from < len(f.Issues); from += archiveChunk {
		to := min(from+archiveChunk, len(f.Issues))
		var res archiveResult
		body := map[string]any{"format": f.Format, "source": f.Source, "exported_at": f.ExportedAt, "issues": f.Issues[from:to]}
		if err := cl.call("POST", base+"/archive/issues", body, &res); err != nil {
			return fmt.Errorf("issues %d to %d: %w (the %d before them are stored; run it again to continue)", from+1, to, err, from)
		}
		total.Issues += res.Issues
		total.Comments += res.Comments
		total.Replaced += res.Replaced
		total.Total = res.Total
	}
	return cl.print(total, *asJSON, func() {
		fmt.Fprintf(cl.out, "Stored %d issues and %d comments (%d replaced an earlier import). %s now holds %d imported issues.\n",
			total.Issues, total.Comments, total.Replaced, pos[0], total.Total)
	})
}

type archivedIssue struct {
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	State        string   `json:"state"`
	Author       string   `json:"author"`
	Body         string   `json:"body"`
	Labels       []string `json:"labels"`
	CreatedAt    string   `json:"created_at"`
	CommentCount int      `json:"comment_count"`
	URL          string   `json:"url"`
	Comments     []struct {
		Author    string `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	} `json:"comments"`
}

func (cl *client) reposArchive(args []string) error {
	if len(args) == 0 {
		return usageErr("launchway repos archive list|show OWNER/NAME ...")
	}
	fs := flag.NewFlagSet("repos archive "+args[0], flag.ContinueOnError)
	state := fs.String("state", "", "open, closed or all")
	query := fs.String("query", "", "text to find in titles, bodies and comments")
	page := fs.Int("page", 1, "page number, from 1")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usageErr("launchway repos archive %s OWNER/NAME ...", args[0])
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	base += "/archive/issues"
	switch args[0] {
	case "list":
		q := url.Values{"page": {strconv.Itoa(*page)}}
		if *state != "" {
			q.Set("state", *state)
		}
		if *query != "" {
			q.Set("q", *query)
		}
		var raw struct {
			Items   []archivedIssue `json:"items"`
			Total   int             `json:"total"`
			Open    int             `json:"open"`
			Closed  int             `json:"closed"`
			Page    int             `json:"page"`
			PerPage int             `json:"per_page"`
		}
		if err := cl.call("GET", base+"?"+q.Encode(), nil, &raw); err != nil {
			return err
		}
		return cl.print(raw, *asJSON, func() {
			tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NUMBER\tSTATE\tCOMMENTS\tAUTHOR\tTITLE")
			for _, it := range raw.Items {
				fmt.Fprintf(tw, "#%d\t%s\t%d\t%s\t%s\n", it.Number, it.State, it.CommentCount, it.Author, it.Title)
			}
			_ = tw.Flush()
			fmt.Fprintf(cl.out, "\n%d matching (%d open, %d closed imported); page %d of %d.\n", raw.Total, raw.Open, raw.Closed, raw.Page, max(1, (raw.Total+raw.PerPage-1)/max(raw.PerPage, 1)))
		})
	case "show":
		if len(pos) != 2 {
			return usageErr("launchway repos archive show OWNER/NAME NUMBER")
		}
		var is archivedIssue
		if err := cl.call("GET", base+"/"+url.PathEscape(pos[1]), nil, &is); err != nil {
			return err
		}
		return cl.print(is, *asJSON, func() {
			fmt.Fprintf(cl.out, "#%d %s [%s] by %s, %s\n", is.Number, is.Title, is.State, is.Author, is.CreatedAt)
			if is.URL != "" {
				fmt.Fprintf(cl.out, "was at %s\n", is.URL)
			}
			fmt.Fprintf(cl.out, "\n%s\n", is.Body)
			for _, c := range is.Comments {
				fmt.Fprintf(cl.out, "\n--- %s, %s\n%s\n", c.Author, c.CreatedAt, c.Body)
			}
		})
	}
	return usageErr("unknown command repos archive %s", args[0])
}
