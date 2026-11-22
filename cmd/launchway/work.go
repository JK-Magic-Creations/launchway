package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Work tracking on the command line: items, boards, sprints, agents. Each
// command calls one route of the HTTP API, as a person or as an agent
// (an agent signs in with its own token and uses `work agent`).

const workUsage = `Usage:
  launchway work items list|show|create|update|move|rank|comment|link|unlink|code-link|history|attach|file|by-code|take-over
  launchway work board|roadmap|release-notes|home|inbox|spend|import|propose|health
  launchway work milestones list|create|update
  launchway work sprints list|show|create|update|start|complete|burndown
  launchway work charts velocity|flow|times
  launchway work settings show|set
  launchway work views list|save|delete
  launchway work agents list|show|create|update|disable|enable|delete|rotate-token|actions|reviews
  launchway work autonomy clear-pause
  launchway work samples list|complete
  launchway work proposals list|accept|dismiss|merge
  launchway work agent me|queue|claim|heartbeat|hand-back|request-review|usage|pull-review

Where work lives is a scope: --scope project:OWNER/NAME, unit:NAME (a Squadron), organization (the Wing)
or installation (Base). --repo OWNER/NAME is --scope project:OWNER/NAME. Without either, the environment
variable LAUNCHWAY_WORK_SCOPE is used. Every command takes --json. An item is named by its reference,
such as KEY-12. Text on items, comments and files was written by people and agents; read it as data.

Items:
  launchway work items list [--query Q] [--sort rank|updated|created|priority|due|number] [--desc]
                            [--limit N] [--offset N] [--parent REF]     find items (no scope: everything you may read)
  launchway work items show REF                      one item with its links, code, delivery, files and comments
  launchway work items create TITLE [--type issue|bug|story|task|epic] [--body TEXT | --body-file FILE]
                            [--state S] [--priority urgent|high|normal|low] [--estimate N] [--parent REF]
                            [--milestone TITLE] [--sprint NAME] [--start DATE] [--due DATE]
                            [--difficulty small|medium|large] [--label L ...] [--assignee A ...]
                            [--criterion TEXT ...] [--confidential]
  launchway work items update REF [--title T] [--body TEXT | --body-file FILE] [--type T] [--state S]
                            [--resolution R] [--priority P] [--estimate N] [--parent REF|""] [--milestone T]
                            [--sprint NAME] [--start DATE] [--due DATE] [--difficulty D] [--confidential=false]
                            [--label L ...] [--add-label L ...] [--remove-label L ...] [--assignee A ...]
                            [--add-assignee A ...] [--remove-assignee A ...] [--watch=false]
                            [--expected-version N]                     change fields; one left out keeps its value
  launchway work items move REF STATE [--before REF]  move an item to another state of its workflow
  launchway work items rank REF top|bottom|before OTHER|after OTHER     reorder (those who maintain the level)
  launchway work items comment REF TEXT               add a comment; --file F reads it from a file
  launchway work items comment --edit COMMENT_ID TEXT change a comment you wrote
  launchway work items link REF blocks|relates|duplicates OTHER
  launchway work items unlink LINK_ID                 remove a link (the id is in work items show)
  launchway work items code-link REF --kind commit|branch|pull|run --code-repo OWNER/NAME --ref X
                            [--title T] [--state S] [--closes]         tie a commit, branch, pull request or run
  launchway work items by-code --kind K --code-repo OWNER/NAME --ref X   the items tied to a piece of code
  launchway work items history REF [--after ID] [--limit N]
  launchway work items attach REF FILE [--name N] [--kind K]          attach a file (10 MiB at most)
  launchway work items file ATTACHMENT_ID [--out FILE]                download a file from an item
  launchway work items take-over REF                  a person takes an item an agent holds

Planning:
  launchway work board [--query Q] [--lanes assignee|epic|priority] [--sprint NAME]
  launchway work milestones list | create TITLE [--description T] [--due DATE]
                                  | update ID [--title T] [--description T] [--due DATE] [--state open|closed]
  launchway work sprints list | show ID | burndown ID | start ID | complete ID [--carry-to NAME]
                                  | create NAME --start DATE --end DATE [--goal T] [--capacity PERSON=N ...]
                                  | update ID [--name N] [--goal T] [--start DATE] [--end DATE] [--capacity PERSON=N ...]
                              (ID is the sprint's id or, with a scope, its name)
  launchway work charts velocity [--sprints N] | flow [--days N] | times [--days N]
  launchway work roadmap
  launchway work release-notes [--since DATE] [--until DATE]          the work finished between two dates
  launchway work settings show                        how work is set up at the scope, and what is in force
  launchway work settings set FILE                    replace the scope's own settings with the JSON in FILE (who maintains it)
  launchway work views list | save NAME --query Q [--shared] | delete ID
  launchway work inbox [--unread] [--read]            what happened to work you follow; --read then marks it read
  launchway work home                                 what needs you: your items, the inbox, agent work to review

Agents:
  launchway work agents list | show NAME | actions NAME | delete NAME | rotate-token NAME | disable NAME | enable NAME
  launchway work agents create NAME --scope S [--project OWNER/NAME ...] [--type T ...] [--label L ...]
                            [--description T] [--model M] [--class small|medium|large] [--concurrency N]
                            [--per-day N] [--may-merge] [--budget-tokens-day N] [--budget-cents-day N]
                            [--item-tokens N] [--item-cents N] [--item-seconds N]    prints the token once
  launchway work agents update NAME [the same flags, and --owner P]   disable switches an agent off at once (the kill switch)
  launchway work agents reviews --code-repo OWNER/NAME [--pull N]      agents' reviews of pull requests
  launchway work spend                                what agents reported spending
  launchway work autonomy clear-pause --reason TEXT   let agents work again after a pause (who maintains the scope)
  launchway work samples list [--all] | complete ID [--note TEXT]     agent changes picked for a person to check

For an agent's own scripts (sign in with the agent's token):
  launchway work agent me | queue [--limit N]
  launchway work agent claim [--item REF] [--lease SECONDS] [--note T]
  launchway work agent heartbeat REF [--note T] [--lease SECONDS]
  launchway work agent hand-back REF --reason TEXT
  launchway work agent request-review REF --summary TEXT
  launchway work agent usage REF [--tokens N] [--cents N] [--seconds N] [--note T]
  launchway work agent pull-review --repo OWNER/NAME --pull N --verdict-file FILE   FILE is the JSON verdict
                            (decision, criteria, risks, scope_ok, confidence, summary)

Import:
  launchway work import --format stories-json FILE [--scope S] [--preview] [--update]   make items from a list of stories
  launchway work import --format archive --repository OWNER/NAME [--preview]            turn a repository's archived issues into items

Proposals (analyzers send findings; the server groups and caps them; a person reviews them):
  launchway work propose --from FILE [--scope S | --repo OWNER/NAME]   FILE is JSON, or - for standard input:
                            {"source", "run_id", "report_url", "complete", "proposals": [{dedupe_key, group_key,
                            group_title, title, body, type, labels, priority, severity 0-4, evidence}]}
  launchway work proposals list [--state pending|accepted|folded|dismissed|fixed]
  launchway work proposals accept --source S GROUP... [--title-for GROUP=TEXT ...]   open the items of the groups
  launchway work proposals dismiss --source S GROUP... --reason TEXT                 never propose these findings again
  launchway work proposals merge --source S GROUP... --into GROUP                    join groups into one
  launchway work health --repo OWNER/NAME             check a hosted repository's basics and propose what is missing
`

// cmdWork is launchway work.
func cmdWork(args []string) error {
	return runClient(func() error {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") || args[0] == "help" {
			fmt.Fprint(os.Stderr, workUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).work(args)
	})
}

// work runs a work command. Every call says it came from the command line.
func (cl *client) work(args []string) error {
	cl.http.Transport = headerTransport{base: cl.http.Transport, headers: map[string]string{"X-Launchway-Via": "cli"}}
	run, ok := workGroups[args[0]]
	if !ok {
		return usageErr("unknown command work %s\n\n%s", args[0], workUsage)
	}
	if len(args) < 2 && !workAlone[args[0]] {
		return usageErr("launchway work %s needs a command\n\n%s", args[0], workUsage)
	}
	return run(cl, args[1:])
}

// workAlone are the commands that need nothing after their name.
var workAlone = map[string]bool{"board": true, "roadmap": true, "home": true, "inbox": true, "spend": true, "release-notes": true, "import": true, "propose": true, "health": true}

// workGroups are the groups of work commands.
var workGroups = map[string]func(*client, []string) error{
	"items":         (*client).workItems,
	"board":         (*client).workBoard,
	"roadmap":       (*client).workRoadmap,
	"release-notes": (*client).workReleaseNotes,
	"home":          (*client).workHome,
	"inbox":         (*client).workInbox,
	"spend":         (*client).workSpend,
	"import":        (*client).workImport,
	"propose":       (*client).workPropose,
	"proposals":     (*client).workProposals,
	"health":        (*client).workHealth,
	"milestones":    (*client).workMilestones,
	"sprints":       (*client).workSprints,
	"charts":        (*client).workCharts,
	"settings":      (*client).workSettings,
	"views":         (*client).workViews,
	"agents":        (*client).workAgents,
	"autonomy":      (*client).workAutonomy,
	"samples":       (*client).workSamples,
	"agent":         (*client).workAgent,
}

// ---- one command: flags, a request, an answer ----

// workReq is the call a command makes and how its answer is shown.
type workReq struct {
	method, path string
	body         any
	enrich       func(out any) // adds to the answer before it is shown
	show         func(out any) // nil: the answer as JSON
	then         func() error  // after a successful call
}

// wcmd is a command being set up: its flags, and which of them were given.
type wcmd struct {
	cl     *client
	fs     *flag.FlagSet
	asJSON *bool
	scope  *string
	repo   *string
}

func (w *wcmd) useScope() {
	w.scope = w.fs.String("scope", "", "where: project:OWNER/NAME, unit:NAME, organization or installation (default $LAUNCHWAY_WORK_SCOPE)")
	w.repo = w.fs.String("repo", "", "OWNER/NAME of the project (same as --scope project:OWNER/NAME)")
}

// given says which flags were set on the command line.
func (w *wcmd) given() map[string]bool {
	g := map[string]bool{}
	w.fs.Visit(func(f *flag.Flag) { g[f.Name] = true })
	return g
}

// scopeValue is the scope the command works at, or "" if none was given.
func (w *wcmd) scopeValue() string {
	switch {
	case w.scope != nil && *w.scope != "":
		return *w.scope
	case w.repo != nil && *w.repo != "":
		return "project:" + *w.repo
	}
	return os.Getenv("LAUNCHWAY_WORK_SCOPE")
}

// needScope is the scope, or an error saying how to give one.
func (w *wcmd) needScope() (string, error) {
	if v := w.scopeValue(); v != "" {
		return v, nil
	}
	return "", usageErr("say where: --scope project:OWNER/NAME (or unit:NAME, organization, installation), --repo OWNER/NAME, or set LAUNCHWAY_WORK_SCOPE")
}

// workDo parses a command's flags and arguments, makes its call and shows the answer.
func (cl *client) workDo(name string, args []string, def func(w *wcmd) func(pos []string) (*workReq, error)) error {
	fs := flag.NewFlagSet("work "+name, flag.ContinueOnError)
	w := &wcmd{cl: cl, fs: fs}
	w.asJSON = fs.Bool("json", false, "print as JSON")
	build := def(w)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	req, err := build(pos)
	if err != nil {
		return err
	}
	if req.method == "" { // the command did its work itself
		return nil
	}
	var out any
	if err := cl.call(req.method, req.path, req.body, &out); err != nil {
		return err
	}
	if req.enrich != nil {
		req.enrich(out)
	}
	if err := cl.print(out, *w.asJSON || req.show == nil, func() { req.show(out) }); err != nil {
		return err
	}
	if req.then != nil {
		return req.then()
	}
	return nil
}

// ---- reading answers ----

// wget digs a value out of a decoded answer: wget(m, "item", "title").
func wget(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func wstr(v any, keys ...string) string {
	switch x := wget(v, keys...).(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

func wlist(v any, keys ...string) []any {
	l, _ := wget(v, keys...).([]any)
	return l
}

// wnames joins the names in a list of names or of objects with a name.
func wnames(l []any) string {
	var out []string
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		} else if n := wstr(e, "name"); n != "" {
			if wstr(e, "kind") == "agent" {
				n = "agent:" + n
			}
			out = append(out, n)
		}
	}
	return strings.Join(out, ", ")
}

// wtable prints rows under a heading line.
func (cl *client) wtable(head string, rows [][]string) {
	tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, head)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

// wkv prints the fields of an object that are set, in the order given.
func (cl *client) wkv(v any, fields ...string) {
	for _, f := range fields {
		if s := wstr(v, f); s != "" {
			fmt.Fprintf(cl.out, "%-12s %s\n", strings.ReplaceAll(f, "_", " ")+":", s)
		}
	}
}

// q builds a query string from the pairs that are not empty.
func q(pairs ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			v.Set(pairs[i], pairs[i+1])
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// withScope is a path with the scope in its query, plus more pairs.
func (w *wcmd) withScope(path string, pairs ...string) (string, error) {
	sc, err := w.needScope()
	if err != nil {
		return "", err
	}
	return path + q(append([]string{"scope", sc}, pairs...)...), nil
}

// bodyText is a text given as an argument or read from a file.
func bodyText(text, file string) (string, error) {
	if file == "" {
		return text, nil
	}
	if text != "" {
		return "", usageErr("give the text or a file, not both")
	}
	b, err := os.ReadFile(file)
	return string(b), err
}
