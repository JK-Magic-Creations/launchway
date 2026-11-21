package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jk-magic-creations/launchway/internal/graph"
)

const graphUsage = `Usage:
  launchway graph [FILE]            print a pipeline's graph as JSON

The software graph (what depends on what, what runs where, what it needs):
  launchway graph summary [--json]
                                    what the graph holds that you may read, and when each source last read
  launchway graph search TEXT [--kind K] [--json]
                                    find nodes by a part of a name (kinds: repository, package, component,
                                    requirement, image, built, pipeline, airfield, deployment, machine,
                                    platform, owner, commit)
  launchway graph show REF [--json]
                                    one node and what is directly connected to it. A REF is kind:key, for
                                    example repository:team/app or package:go:example.com/lib
  launchway graph impact REF [--depth N] [--nodes N] [--as-of TIME | --commit SHA -R OWNER/NAME] [--json]
                                    what is affected if this changes: the closure with the way to each thing,
                                    grouped by owner, with the deployments that run affected digests
  launchway graph where REF [--json]
                                    where is this running: the Airfields and digests of the deployments a
                                    repository, package, commit or image reaches
  launchway graph contents REF [--json]
                                    what is in this: for a deployment or an image, the images, base images,
                                    packages with their versions, commits and repositories behind it
  launchway graph path FROM TO [--json]
                                    how two nodes are connected, the shortest way, with the edges
  launchway graph order [-R OWNER/NAME]... [--upstream] [--json]
                                    the order repositories are built in (providers first); cycles are shown
                                    with the path; overrides are marked
  launchway graph rebuild-set -R OWNER/NAME [--json]
                                    what must be built again after a push to this repository, in order
  launchway graph overrides [--json]
  launchway graph override add --kind before|ignore|pin --from OWNER/NAME --to OWNER/NAME --reason TEXT [--value VERSION]
  launchway graph override remove ID
                                    list, add and remove your corrections to the build order. before: FROM is
                                    built before TO. ignore: FROM does not depend on TO. pin: FROM stays at
                                    VERSION of TO, so a push to TO does not rebuild it. A reason is required
  launchway graph needs REF [--json]
                                    what a component (or each component of a repository) needs, declared and
                                    inferred, with where each fact came from
  launchway graph placement REF [--json]
                                    which machines and platforms satisfy it, near misses, and the gaps
  launchway graph spec REF... [--headroom PERCENT] [--format json|yaml]
                                    the machine specification for components that run together: architecture,
                                    CPU, memory and storage with headroom, devices, operating-system features,
                                    ports and data services. An export; nothing is provisioned from it
  launchway graph reach REF [--json]
                                    which deployments run a version of this package that has an advisory
  launchway graph propose-requirements -R OWNER/NAME --file FILE [--title TEXT]
                                    open a pull request that makes .launchway/requirements.yaml what FILE says
  launchway graph refresh -R OWNER/NAME
  launchway graph rescan | rebuild
                                    read one repository again; read everything again; clear the graph and read
                                    everything again (administrators). The overrides are kept
`

// graphSubcommands are the words after "launchway graph" that ask the software
// graph; anything else is the older "launchway graph FILE", which prints a
// pipeline's graph.
var graphSubcommands = map[string]bool{"summary": true, "search": true, "show": true, "impact": true, "where": true, "contents": true, "path": true,
	"order": true, "rebuild-set": true, "overrides": true, "override": true, "needs": true, "placement": true, "spec": true, "reach": true,
	"propose-requirements": true, "refresh": true, "rescan": true, "rebuild": true}

func init() { helpTexts["graph"] = graphUsage }

func cmdSoftwareGraph(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, graphUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		cl.http.Timeout = 5 * time.Minute
		return cl.graph(args[0], args[1:])
	})
}

// graphFlags are the flags every graph question shares.
type graphFlags struct {
	fs                  *flag.FlagSet
	asJSON              *bool
	depth, nodes        *int
	asOf, commit, repo  *string
	kind                *string
	upstream            *bool
	headroom            *int
	format, file, title *string
	from, to, reason    *string
	value               *string
	repos               repoList
	pos                 []string
}

type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(s string) error { *r = append(*r, s); return nil }

func newGraphFlags(name string) *graphFlags {
	f := &graphFlags{fs: flag.NewFlagSet("graph "+name, flag.ContinueOnError)}
	f.asJSON = f.fs.Bool("json", false, "print as JSON")
	f.depth = f.fs.Int("depth", 0, "follow at most this many steps")
	f.nodes = f.fs.Int("nodes", 0, "return at most this many nodes")
	f.asOf = f.fs.String("as-of", "", "answer as of a time (2026-11-01T09:00:00Z)")
	f.commit = f.fs.String("commit", "", "answer as of the graph's reading of this commit (with -R)")
	f.repo = f.fs.String("R", "", "the repository, OWNER/NAME")
	f.fs.Var(&f.repos, "repository", "a repository, OWNER/NAME (repeat for several)")
	f.kind = f.fs.String("kind", "", "a kind")
	f.upstream = f.fs.Bool("upstream", false, "include what the repositories depend on")
	f.headroom = f.fs.Int("headroom", 0, "percent added to CPU, memory and storage (default 25)")
	f.format = f.fs.String("format", "json", "json or yaml")
	f.file = f.fs.String("file", "", "a file")
	f.title = f.fs.String("title", "", "a title")
	f.from = f.fs.String("from", "", "")
	f.to = f.fs.String("to", "", "")
	f.reason = f.fs.String("reason", "", "why")
	f.value = f.fs.String("value", "", "a version")
	return f
}

func (f *graphFlags) parse(args []string) error {
	pos, err := parseArgs(f.fs, args)
	if err != nil {
		return err
	}
	f.pos = pos
	return nil
}

func (f *graphFlags) query() url.Values {
	q := url.Values{}
	if *f.depth > 0 {
		q.Set("depth", strconv.Itoa(*f.depth))
	}
	if *f.nodes > 0 {
		q.Set("nodes", strconv.Itoa(*f.nodes))
	}
	if *f.asOf != "" {
		q.Set("as_of", *f.asOf)
	}
	if *f.commit != "" {
		q.Set("commit", *f.commit)
		q.Set("repository", *f.repo)
	}
	return q
}

func (f *graphFlags) one(what string) (string, error) {
	if len(f.pos) != 1 {
		return "", usageErr("name %s (kind:key, for example repository:team/app)", what)
	}
	if _, err := graph.ParseRef(f.pos[0]); err != nil {
		return "", usageErr("%v", err)
	}
	return f.pos[0], nil
}

func (cl *client) gget(path string, q url.Values, v any) error {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return cl.call("GET", "/api/v1/graph"+path, nil, v)
}

// graphCommands is each word after "launchway graph" and what runs it.
var graphCommands = map[string]func(cl *client, cmd string, f *graphFlags) error{
	"summary":              (*client).graphSummary,
	"search":               (*client).graphSearch,
	"show":                 (*client).graphShow,
	"impact":               (*client).graphImpact,
	"where":                (*client).graphWhere,
	"contents":             (*client).graphContents,
	"path":                 (*client).graphPath,
	"order":                (*client).graphOrder,
	"rebuild-set":          (*client).graphRebuildSet,
	"overrides":            (*client).graphOverrides,
	"override-add":         (*client).graphOverrideAdd,
	"override-remove":      (*client).graphOverrideRemove,
	"needs":                (*client).graphNeeds,
	"placement":            (*client).graphNeeds,
	"spec":                 (*client).graphSpec,
	"reach":                (*client).graphReach,
	"propose-requirements": (*client).graphProposeRequirements,
	"refresh":              (*client).graphRefresh,
	"rescan":               (*client).graphRescan,
	"rebuild":              (*client).graphRescan,
}

func (cl *client) graph(cmd string, args []string) error {
	if cmd == "override" {
		if len(args) == 0 {
			return usageErr("override add or override remove\n\n%s", graphUsage)
		}
		cmd, args = "override-"+args[0], args[1:]
	}
	run, ok := graphCommands[cmd]
	if !ok {
		return usageErr("unknown command graph %s\n\n%s", cmd, graphUsage)
	}
	f := newGraphFlags(cmd)
	if err := f.parse(args); err != nil {
		return err
	}
	return run(cl, cmd, f)
}

func (cl *client) graphSummary(cmd string, f *graphFlags) error {
	_ = cmd
	var s graph.Summary
	if err := cl.gget("", nil, &s); err != nil {
		return err
	}
	return cl.print(s, *f.asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
		for _, k := range graph.NodeKinds {
			if n := s.Nodes[string(k)]; n > 0 {
				fmt.Fprintf(tw, "%s\t%d\n", k, n)
			}
		}
		fmt.Fprintf(tw, "edges\t%d\n", s.Edges)
		tw.Flush()
		for _, r := range s.Sources {
			fmt.Fprintf(cl.out, "source %s read %d scopes, last at %s\n", r.Source, r.Scopes, time.UnixMilli(r.At).Format(time.RFC3339))
		}
	})
}

func (cl *client) graphSearch(cmd string, f *graphFlags) error {
	_ = cmd
	if len(f.pos) != 1 {
		return usageErr("name what to search for")
	}
	q := url.Values{"q": {f.pos[0]}}
	if *f.kind != "" {
		q.Set("kind", *f.kind)
	}
	var out struct {
		Items []graph.Node `json:"items"`
		More  bool         `json:"more"`
	}
	if err := cl.gget("/search", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		for _, n := range out.Items {
			fmt.Fprintf(cl.out, "%s:%s\n", n.Kind, n.Key)
		}
		if out.More {
			fmt.Fprintln(cl.out, "(more: narrow the search)")
		}
	})
}

func (cl *client) graphShow(cmd string, f *graphFlags) error {
	_ = cmd
	ref, err := f.one("the node")
	if err != nil {
		return err
	}
	var out struct {
		Node  graph.Node       `json:"node"`
		Edges []graph.EdgeView `json:"edges"`
	}
	q := f.query()
	q.Set("ref", ref)
	if err := cl.gget("/node", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		fmt.Fprintf(cl.out, "%s:%s (%s, from %s)\n", out.Node.Kind, out.Node.Key, out.Node.Confidence, out.Node.Origin)
		for _, e := range out.Edges {
			fmt.Fprintf(cl.out, "  %s:%s  --%s-->  %s:%s %s\n", e.From.Kind, e.From.Key, e.Kind, e.To.Kind, e.To.Key, edgeNote(e))
		}
	})
}

func (cl *client) graphImpact(cmd string, f *graphFlags) error {
	_ = cmd
	ref, err := f.one("what changes")
	if err != nil {
		return err
	}
	var out graph.ImpactResult
	q := f.query()
	q.Set("ref", ref)
	if err := cl.gget("/impact", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { printImpact(cl, out) })
}

func (cl *client) graphWhere(cmd string, f *graphFlags) error {
	_ = cmd
	ref, err := f.one("the thing")
	if err != nil {
		return err
	}
	var out graph.WhereResult
	q := f.query()
	q.Set("ref", ref)
	if err := cl.gget("/where", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if len(out.Deployments) == 0 {
			fmt.Fprintf(cl.out, "%s:%s is not running anywhere the graph knows of\n", out.Start.Kind, out.Start.Key)
		}
		for _, d := range out.Deployments {
			fmt.Fprintf(cl.out, "%-12s %-34s %s  %s %s\n", d.Airfield, d.Deployment.Key, shortDigest(d.Digest), d.Version, exactNote(d.Exact))
		}
		truncNote(cl, out.Truncation)
	})
}

func (cl *client) graphContents(cmd string, f *graphFlags) error {
	_ = cmd
	ref, err := f.one("the deployment, image or component")
	if err != nil {
		return err
	}
	var out graph.ContentsResult
	q := f.query()
	q.Set("ref", ref)
	if err := cl.gget("/contents", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		for _, g := range []struct {
			name string
			hits []graph.Hit
		}{{"images", out.Images}, {"base images", out.BaseImages}, {"packages", out.Packages}, {"commits", out.Commits}, {"repositories", out.Repositories}} {
			fmt.Fprintf(cl.out, "%s (%d)\n", g.name, len(g.hits))
			for i, h := range g.hits {
				if i >= 50 {
					fmt.Fprintf(cl.out, "  ... and %d more\n", len(g.hits)-i)
					break
				}
				fmt.Fprintf(cl.out, "  %s %s\n", h.Node.Key, lastResolved(h))
			}
		}
		truncNote(cl, out.Truncation)
	})
}

func (cl *client) graphPath(cmd string, f *graphFlags) error {
	_ = cmd
	if len(f.pos) != 2 {
		return usageErr("name two nodes: launchway graph path FROM TO")
	}
	var out graph.PathResult
	q := f.query()
	q.Set("from", f.pos[0])
	q.Set("to", f.pos[1])
	if err := cl.gget("/path", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if !out.Found {
			fmt.Fprintln(cl.out, "no path between them that you may see")
			return
		}
		fmt.Fprintf(cl.out, "%s:%s (%s)\n", out.From.Kind, out.From.Key, out.Way)
		for _, s := range out.Steps {
			fmt.Fprintf(cl.out, "  -> %s:%s   via %s %s\n", s.Node.Kind, s.Node.Key, s.Edge.Kind, edgeNote(s.Edge))
		}
	})
}

func (cl *client) graphOrder(cmd string, f *graphFlags) error {
	_ = cmd
	q := f.query()
	for _, r := range f.repos {
		q.Add("repository", r)
	}
	if *f.repo != "" && *f.commit == "" {
		q.Add("repository", *f.repo)
	}
	if *f.upstream {
		q.Set("upstream", "true")
	}
	var out graph.OrderResult
	if err := cl.gget("/order", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { printOrder(cl, out) })
}

func (cl *client) graphRebuildSet(cmd string, f *graphFlags) error {
	_ = cmd
	if *f.repo == "" {
		return usageErr("name the repository that was pushed: -R OWNER/NAME")
	}
	q := f.query()
	q.Set("repository", *f.repo)
	var out graph.RebuildResult
	if err := cl.gget("/rebuild-set", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if len(out.Items) == 0 {
			fmt.Fprintf(cl.out, "nothing else must be built after a push to %s\n", out.Repository)
		}
		for _, it := range out.Items {
			var via []string
			for _, w := range it.Why {
				via = append(via, w.Provider+" ("+strings.Join(w.Via, ", ")+")")
			}
			fmt.Fprintf(cl.out, "%d  %s   after %s\n", it.Level, it.Repository, strings.Join(via, " then "))
		}
		for _, s := range out.Skipped {
			fmt.Fprintf(cl.out, "not rebuilt: %s (%s: %s)\n", s.Consumer, s.Override, s.Reason)
		}
		truncNote(cl, out.Truncation)
	})
}

func (cl *client) graphOverrides(cmd string, f *graphFlags) error {
	_ = cmd
	var out struct {
		Items []graph.Override `json:"items"`
	}
	if err := cl.gget("/overrides", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		for _, o := range out.Items {
			fmt.Fprintf(cl.out, "%d  %s %s -> %s %s  because %s (%s)\n", o.ID, o.Kind, o.From, o.To, o.Value, o.Reason, o.Author)
		}
	})
}

func (cl *client) graphOverrideAdd(cmd string, f *graphFlags) error {
	_ = cmd
	kind := *f.kind
	var out graph.Override
	if err := cl.call("POST", "/api/v1/graph/overrides", graph.Override{Kind: kind, From: *f.from, To: *f.to, Value: *f.value, Reason: *f.reason}, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { fmt.Fprintf(cl.out, "override %d recorded\n", out.ID) })
}

func (cl *client) graphOverrideRemove(cmd string, f *graphFlags) error {
	_ = cmd
	if len(f.pos) != 1 {
		return usageErr("name the override's number")
	}
	if err := cl.call("DELETE", "/api/v1/graph/overrides/"+f.pos[0], nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(cl.out, "override removed")
	return nil
}

func (cl *client) graphNeeds(cmd string, f *graphFlags) error {
	_ = cmd
	ref, err := f.one("the component or repository")
	if err != nil {
		return err
	}
	q := f.query()
	q.Set("ref", ref)
	if cmd == "needs" {
		var out struct {
			Items []graph.ComponentNeeds `json:"items"`
			File  string                 `json:"file"`
		}
		if err := cl.gget("/needs", q, &out); err != nil {
			return err
		}
		return cl.print(out, *f.asJSON, func() {
			for _, n := range out.Items {
				fmt.Fprintf(cl.out, "%s\n", n.Component.Key)
				for _, x := range n.Facts {
					fmt.Fprintf(cl.out, "  %-9s %-22s %-9s %s\n", x.Facet, x.Display(), x.Source, x.Evidence)
				}
				for _, a := range n.Advice {
					fmt.Fprintf(cl.out, "  advice: %s\n", a.Message)
				}
			}
			fmt.Fprintf(cl.out, "\nwrite what is wrong in %s; what is declared there always wins\n", out.File)
		})
	}
	var out struct {
		Items []graph.PlacementResult `json:"items"`
	}
	if err := cl.gget("/placement", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		for _, p := range out.Items {
			fmt.Fprintf(cl.out, "%s: %d of %d hosts satisfy it\n", p.Component.Key, len(p.Fits), p.Hosts)
			for _, c := range p.Fits {
				fmt.Fprintf(cl.out, "  fits      %s:%s\n", c.Host.Kind, c.Host.Key)
			}
			for _, c := range p.NearMisses {
				var why []string
				for _, m := range c.Missing {
					why = append(why, m.Reason)
				}
				fmt.Fprintf(cl.out, "  near miss %s:%s  %s\n", c.Host.Kind, c.Host.Key, strings.Join(why, "; "))
			}
			for _, g := range p.Gaps {
				fmt.Fprintf(cl.out, "  GAP       %s\n", g.Message)
			}
		}
	})
}

func (cl *client) graphSpec(cmd string, f *graphFlags) error {
	_ = cmd
	if len(f.pos) == 0 {
		return usageErr("name the components or repositories that run together")
	}
	q := f.query()
	for _, r := range f.pos {
		q.Add("ref", r)
	}
	if *f.headroom > 0 {
		q.Set("headroom", strconv.Itoa(*f.headroom))
	}
	var out graph.MachineSpec
	if err := cl.gget("/specification", q, &out); err != nil {
		return err
	}
	if *f.format == "yaml" {
		y, err := out.YAML()
		if err != nil {
			return err
		}
		fmt.Fprint(cl.out, y)
		return nil
	}
	return cl.print(out, true, nil)
}

func (cl *client) graphReach(cmd string, f *graphFlags) error {
	_ = cmd
	ref, err := f.one("the package")
	if err != nil {
		return err
	}
	q := f.query()
	q.Set("ref", ref)
	var out graph.ReachResult
	if err := cl.gget("/reach", q, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() {
		if out.Note != "" {
			fmt.Fprintln(cl.out, out.Note)
		}
		for _, v := range out.Versions {
			var ids []string
			for _, a := range v.Advisories {
				ids = append(ids, a.ID)
			}
			fmt.Fprintf(cl.out, "%s %s: %s\n", out.Package.Key, v.Version, strings.Join(ids, ", "))
			for _, d := range v.Deployments {
				fmt.Fprintf(cl.out, "  runs in %s %s\n", d.Airfield, d.Deployment.Key)
			}
		}
	})
}

func (cl *client) graphProposeRequirements(cmd string, f *graphFlags) error {
	_ = cmd
	if *f.repo == "" || *f.file == "" {
		return usageErr("give -R OWNER/NAME and --file FILE")
	}
	data, err := os.ReadFile(*f.file) //nolint:gosec // G703: the file the person named on their own command line
	if err != nil {
		return err
	}
	var out struct {
		Number int    `json:"number"`
		Branch string `json:"branch"`
	}
	if err := cl.call("POST", "/api/v1/repositories/"+*f.repo+"/requirements/proposal", map[string]string{"content": string(data), "title": *f.title}, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "opened pull request #%d from %s\n", out.Number, out.Branch)
	return nil
}

func (cl *client) graphRefresh(cmd string, f *graphFlags) error {
	_ = cmd
	if *f.repo == "" {
		return usageErr("name the repository: -R OWNER/NAME")
	}
	var out graph.RunResult
	if err := cl.call("POST", "/api/v1/repositories/"+*f.repo+"/graph/refresh", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *f.asJSON, func() { fmt.Fprintf(cl.out, "read %s again\n", out.Scope) })
}

func (cl *client) graphRescan(cmd string, f *graphFlags) error {
	_ = cmd
	var out struct {
		Scopes int `json:"scopes"`
	}
	if err := cl.call("POST", "/api/v1/graph/"+cmd, nil, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%s: read %d scopes\n", cmd, out.Scopes)
	return nil
}

func edgeNote(e graph.EdgeView) string {
	var s []string
	if e.Constraint != "" {
		s = append(s, "wants "+e.Constraint)
	}
	if e.Resolved != "" {
		s = append(s, "resolved "+e.Resolved)
	}
	if e.Qualifier != "" {
		s = append(s, "["+e.Qualifier+"]")
	}
	return strings.Join(s, " ")
}

func exactNote(exact bool) string {
	if exact {
		return "(in the digest that runs)"
	}
	return "(through a dependency or a build)"
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func lastResolved(h graph.Hit) string {
	if len(h.Path) > 0 {
		if r := h.Path[0].Edge.Resolved; r != "" {
			return r
		}
	}
	return ""
}

func truncNote(cl *client, t graph.Truncation) {
	if t.Truncated {
		fmt.Fprintf(cl.out, "\n(not the whole answer: stopped at the %s limit; raise --depth or --nodes)\n", t.Reason)
	}
}

func printImpact(cl *client, out graph.ImpactResult) {
	fmt.Fprintf(cl.out, "A change to %s:%s reaches %d things", out.Start.Kind, out.Start.Key, out.Total)
	if len(out.Repositories) > 0 {
		fmt.Fprintf(cl.out, " in %d other repositories", len(out.Repositories))
	}
	fmt.Fprintln(cl.out)
	for _, g := range out.ByOwner {
		fmt.Fprintf(cl.out, "\n%s (%d)\n", g.Owner, g.Count)
		for i, it := range g.Items {
			if i >= 20 {
				fmt.Fprintf(cl.out, "  ... and %d more\n", len(g.Items)-i)
				break
			}
			var via []string
			for _, s := range it.Path {
				via = append(via, string(s.Edge.Kind))
			}
			fmt.Fprintf(cl.out, "  %-11s %s   (%s)\n", it.Node.Kind, it.Node.Key, strings.Join(via, " > "))
		}
	}
	if len(out.Deployments) > 0 {
		fmt.Fprintln(cl.out, "\nRunning")
		for _, d := range out.Deployments {
			fmt.Fprintf(cl.out, "  %-12s %-34s %s  %s\n", d.Airfield, d.Deployment.Key, shortDigest(d.Digest), d.Version)
		}
	}
	truncNote(cl, out.Truncation)
}

func printOrder(cl *client, out graph.OrderResult) {
	for i, l := range out.Levels {
		fmt.Fprintf(cl.out, "%2d  %s\n", i+1, strings.Join(l, "  "))
	}
	for _, c := range out.Cycles {
		fmt.Fprintf(cl.out, "\nCYCLE  %s\n       %s\n", strings.Join(c.Path, " -> "), c.Suggestion)
	}
	for _, o := range out.Overrides {
		fmt.Fprintf(cl.out, "\noverride %d: %s %s -> %s %s: %s (%s)\n", o.ID, o.Kind, o.From, o.To, o.Value, o.Reason, o.Author)
	}
	for _, u := range out.Unknown {
		fmt.Fprintf(cl.out, "\nnot found (or not yours to read): %s\n", u)
	}
	truncNote(cl, out.Truncation)
}
