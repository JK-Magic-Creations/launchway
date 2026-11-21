package pipeline

// Drift check between the parser and docs/reference/pipeline.md.
//
// The reference documents every key the parser accepts, in tables marked
// with an HTML comment on the line before the table:
//
//	<!-- keys: NAME -->
//	| Key | ... |
//	|---|---|
//	| `key` | ... |
//
// Each row of a marked table starts with the key in a code span. This test
// works out, for every NAME, the keys the parser accepts there, and fails
// when the table lists a key the parser does not accept or leaves out one
// it does.
//
// How the accepted keys are found:
//
//   - Mappings checked by eachKey (on, when, artifacts, argocd, ...) are
//     probed: an unknown key is added to a valid pipeline (or template) at
//     that place, and the parser's "expected one of: ..." message gives the
//     keys it accepts. This exercises the parser itself, not a copy of it.
//   - The keys of each step kind come from the parser's own lists
//     (stepKeys, runOnly, useKeys, imageKeys, deployKeys, ...), combined the
//     way parseSteps combines them. The table step-common has a second
//     column naming the kinds each row applies to; a kind's keys are those
//     rows plus its own table (kind-<kind>).
//   - To catch a new mapping nobody probes yet, every list of keys written
//     in the package's source (a []string literal given to eachKey, or a
//     package-level variable named ...Keys) must fit within one documented
//     table.
//
// It also parses every ```yaml block that a <!-- validate --> line precedes
// (a whole pipeline), and every block that <!-- validate-template --> precedes
// (a whole template, read with its parameters' defaults).

import (
	"bufio"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const pipelineReference = "../../docs/reference/pipeline.md"

// stepKinds are the kinds of step the reference documents; "uses" is a step
// that uses a template.
var stepKinds = []string{"step", "approval", "uses", "image", "deploy", "rollback", "rollout", "ai", "verify"}

// docRow is one row of a marked table.
type docRow struct {
	key   string
	cells []string
	line  int
}

var (
	keysMarkerRe = regexp.MustCompile(`^<!-- keys: ([a-z0-9_.:-]+) -->\s*$`)
	rowKeyRe     = regexp.MustCompile("^`([^`]+)`")
	codeSpanRe   = regexp.MustCompile("`([^`]+)`")
)

// readKeyTables reads the marked tables of a reference page.
func readKeyTables(t *testing.T, file string) map[string][]docRow {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatalf("the reference is missing: %v", err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	out := map[string][]docRow{}
	for i := 0; i < len(lines); i++ {
		m := keysMarkerRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		name := m[1]
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j+1 >= len(lines) || !strings.HasPrefix(lines[j], "|") || !strings.HasPrefix(lines[j+1], "|") {
			t.Errorf("%s:%d: <!-- keys: %s --> is not followed by a table", file, i+1, name)
			continue
		}
		if _, ok := out[name]; !ok {
			out[name] = nil
		}
		for j += 2; j < len(lines) && strings.HasPrefix(lines[j], "|"); j++ {
			cells := splitRow(lines[j])
			k := rowKeyRe.FindStringSubmatch(cells[0])
			if k == nil {
				t.Errorf("%s:%d: a row of table %s does not start with a key in a code span", file, j+1, name)
				continue
			}
			for _, r := range out[name] {
				if r.key == k[1] {
					t.Errorf("%s:%d: table %s lists %s twice (also on line %d)", file, j+1, name, k[1], r.line)
				}
			}
			out[name] = append(out[name], docRow{key: k[1], cells: cells, line: j + 1})
		}
	}
	return out
}

// splitRow splits a Markdown table row into its cells, keeping \| inside
// a cell.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			b.WriteString(`\|`)
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteByte(line[i])
	}
	return append(cells, strings.TrimSpace(b.String()))
}

func keysOf(rows []docRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.key
	}
	sort.Strings(out)
	return out
}

func sortedSet(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, k := range l {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func minus(list []string, remove ...[]string) []string {
	var out []string
	for _, k := range list {
		drop := false
		for _, r := range remove {
			drop = drop || slices.Contains(r, k)
		}
		if !drop {
			out = append(out, k)
		}
	}
	return sortedSet(out)
}

// ---- probes ----

const probeKey = "zzprobe"

// probePipeline is a valid pipeline that reaches every mapping the parser
// checks with a list of keys.
const probePipeline = `
pipeline: probe
on:
  push: { branches: [main] }
  pull_request: {}
  schedule:
    - { cron: "0 3 * * *" }
  webhook: { name: hook }
inputs:
  target: { type: choice, options: [prod, staging], default: prod }
  systems: { type: list, default: [debian] }
libraries:
  platform: { repo: ci-templates, version: v3 }
containers:
  py: { image: python:3.13 }
runner: { pool: gpu, labels: [cuda] }
lane: { name: probe, per: [ref], when_busy: wait }
steps:
  build:
    image: alpine:3.22
    run: "true"
    when: { branch: main }
    retry: { times: 2, wait: 10s }
    resources: { cpu: 2, memory: 4Gi }
    secrets:
      TOKEN: { store: vault, key: ci/token, field: token }
    reports:
      tests: { paths: reports/*.xml, attachments: test-results }
      coverage: { paths: coverage/lcov.info, fail_under: { lines: 80 } }
      html: [{ name: pw, path: playwright-report, index: index.html }]
    artifacts:
      - { name: bin, paths: [bin/app] }
  test:
    container: py
    needs: build
    run: "true"
    fetch:
      - { from: build, name: bin }
  matrix:
    needs: build
    for_each:
      os: { input: systems }
      python: ["3.12"]
    image: { each: python, format: "python:%s" }
    run: "true"
  ci:
    uses: platform/check
    needs: build
    with:
      target: { input: target }
  image:
    kind: image
    needs: test
    tags:
      - { registry: harbor, repository: team/app, tag: { run: short_sha } }
    cache: { registry: harbor, repository: team/cache }
    secrets:
      - { id: npm, secret: { store: vault, key: ci/npm, field: token } }
    sign: { key: { store: vault, key: ci/cosign, field: key } }
    scan: { mode: report }
  deploy:
    kind: deploy
    needs: image
    environment: { input: target }
    version: { run: short_sha }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/app, digest: { output: image.digest } }
      sync:
        resources:
          - { kind: Rollout, name: shop, namespace: shop }
    rollout: { name: shop, namespace: shop }
  smoke:
    image: alpine:3.22
    needs: deploy
    run: "true"
  approve:
    kind: approval
    needs: smoke
    deployment: deploy
    message: Promote?
  promote:
    kind: rollout
    needs: approve
    deployment: deploy
    action: promote
  rollback:
    kind: rollback
    deployment: deploy
    needs: [promote]
  verify:
    kind: verify
    needs: deploy
    deployment: deploy
    params:
      ns: { deployment: namespace }
    metrics:
      - { name: error-rate, source: prometheus, query: 'sum(rate(errors{namespace="{{ns}}"}[1m]))', max: 0.01, fail_fast: { max: 0.5 } }
    logs:
      - { name: errors, source: loki, query: '{namespace="{{ns}}"}' }
  notes:
    kind: ai
    needs: smoke
    model: local
    prompt: Summarise the release.
    inputs:
      - { label: version, output: build.version }
`

// probeTemplate is a valid template, read for a use with no values.
const probeTemplate = `
template: check
description: probe
params:
  python: { type: string, default: "3.13", options: ["3.12", "3.13"] }
  scan: { type: boolean, default: true }
  pythons: { type: list, default: ["3.12"] }
containers:
  py: { image: { param: python, format: "python:%s" } }
steps:
  lint: { container: py, run: "true" }
  scan:
    container: py
    needs: lint
    if: { param: scan }
    run: "true"
  loop:
    needs: lint
    for_each: { v: { param: pythons } }
    image: alpine:3.22
    run: "true"
  img:
    kind: image
    needs: lint
    push: false
outputs:
  result: { from: lint.result }
`

// probe names a place in probePipeline or probeTemplate, and the table that
// documents the keys accepted there.
type probe struct {
	table    string
	template bool
	path     []any // mapping keys and sequence indexes
	// minus, if set, is taken away from what the parser accepts there: the
	// table documents only the rest.
	minus []string
}

var probes = []probe{
	{table: "top"},
	{table: "on", path: []any{"on"}},
	{table: "on-push", path: []any{"on", "push"}},
	{table: "on-pull-request", path: []any{"on", "pull_request"}},
	{table: "on-schedule", path: []any{"on", "schedule", 0}},
	{table: "on-webhook", path: []any{"on", "webhook"}},
	{table: "inputs", path: []any{"inputs", "target"}},
	{table: "libraries", path: []any{"libraries", "platform"}},
	{table: "containers", path: []any{"containers", "py"}},
	{table: "secret-ref", path: []any{"steps", "build", "secrets", "TOKEN"}},
	{table: "secret-ref", path: []any{"steps", "image", "secrets", 0, "secret"}},
	{table: "secret-ref", path: []any{"steps", "image", "sign", "key"}},
	{table: "when", path: []any{"steps", "build", "when"}},
	{table: "retry", path: []any{"steps", "build", "retry"}},
	{table: "reports", path: []any{"steps", "build", "reports"}},
	{table: "reports-tests", path: []any{"steps", "build", "reports", "tests"}},
	{table: "reports-coverage", path: []any{"steps", "build", "reports", "coverage"}},
	{table: "reports-fail-under", path: []any{"steps", "build", "reports", "coverage", "fail_under"}},
	{table: "reports-html", path: []any{"steps", "build", "reports", "html", 0}},
	{table: "runner", path: []any{"runner"}},
	{table: "lane", path: []any{"lane"}},
	{table: "step-resources", path: []any{"steps", "build", "resources"}},
	{table: "ai-input", path: []any{"steps", "notes", "inputs", 0}},
	{table: "verify-metric", path: []any{"steps", "verify", "metrics", 0}},
	{table: "verify-log", path: []any{"steps", "verify", "logs", 0}},
	{table: "artifacts", path: []any{"steps", "build", "artifacts", 0}},
	{table: "fetch", path: []any{"steps", "test", "fetch", 0}},
	{table: "value", path: []any{"steps", "deploy", "version"}},
	{table: "value", path: []any{"steps", "image", "tags", 0, "tag"}},
	{table: "value-environment", path: []any{"steps", "deploy", "environment"}},
	{table: "value-with-input", path: []any{"steps", "ci", "with", "target"}},
	{table: "value-each", path: []any{"steps", "matrix", "image"}},
	{table: "loop-list-input", path: []any{"steps", "matrix", "for_each", "os"}},
	{table: "argocd", path: []any{"steps", "deploy", "argocd"}},
	{table: "write-back", path: []any{"steps", "deploy", "argocd", "write_back"}},
	{table: "change", path: []any{"steps", "deploy", "argocd", "write_back", "changes", 0}},
	{table: "sync", path: []any{"steps", "deploy", "argocd", "sync"}},
	{table: "sync-resource", path: []any{"steps", "deploy", "argocd", "sync", "resources", 0}},
	{table: "rollout-ref", path: []any{"steps", "deploy", "rollout"}},
	{table: "image-tag", path: []any{"steps", "image", "tags", 0}},
	{table: "image-cache", path: []any{"steps", "image", "cache"}},
	{table: "image-secret", path: []any{"steps", "image", "secrets", 0}},
	{table: "image-sign", path: []any{"steps", "image", "sign"}},
	{table: "image-scan", path: []any{"steps", "image", "scan"}},

	{table: "template-top", template: true},
	{table: "template-param", template: true, path: []any{"params", "python"}},
	{table: "template-output", template: true, path: []any{"outputs", "result"}},
	{table: "template-if", template: true, path: []any{"steps", "scan", "if"}},
	{table: "value-param", template: true, path: []any{"containers", "py", "image"}},
	{table: "loop-list-param", template: true, path: []any{"steps", "loop", "for_each", "v"}},
	{table: "template-step-extra", template: true, path: []any{"steps", "lint"}, minus: stepKeys},
	{table: "template-step-extra", template: true, path: []any{"steps", "img"}, minus: imageKeys},
}

var expectedOneOfRe = regexp.MustCompile(`expected one of: (.+)$`)

// accepted adds an unknown key at the probe's place and returns the keys
// the parser says it expects there.
func accepted(t *testing.T, pr probe) []string {
	t.Helper()
	text := probePipeline
	if pr.template {
		text = probeTemplate
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	n := doc.Content[0]
	for _, p := range pr.path {
		switch p := p.(type) {
		case string:
			var next *yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == p {
					next = n.Content[i+1]
				}
			}
			if next == nil {
				t.Fatalf("probe %s: no key %q on the way to %v", pr.table, p, pr.path)
			}
			n = next
		case int:
			n = n.Content[p]
		}
	}
	if n.Kind != yaml.MappingNode {
		t.Fatalf("probe %s: %v is not a mapping", pr.table, pr.path)
	}
	n.Style = 0 // block style, so the added key reads back
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: probeKey}, &yaml.Node{Kind: yaml.ScalarNode, Value: "1"})
	data, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	var problems Problems
	if pr.template {
		_, problems = readTemplate(data, "check", nil, nil, 1, func(int, int, string, string) {}, DefaultMaxLoopMembers, "ci/")
	} else {
		_, err := Parse(data)
		problems, _ = err.(Problems)
	}
	var found []string
	for _, p := range problems {
		if !strings.HasSuffix(p.Path, probeKey) {
			continue
		}
		m := expectedOneOfRe.FindStringSubmatch(p.Message)
		if m == nil {
			continue
		}
		found = sortedSet(found, strings.Split(m[1], ", "))
	}
	if found == nil {
		t.Fatalf("probe %s at %v: the parser gave no list of expected keys; problems: %v", pr.table, pr.path, problems)
	}
	return minus(found, pr.minus)
}

// kindKeys are the keys each kind of step accepts in a pipeline, combined
// from the parser's lists as parseSteps, parseUseStep, parseApproval and
// checkDeployStep combine them.
func kindKeys() map[string][]string {
	return map[string][]string{
		// uses makes a step a uses step, and with goes with uses.
		"step":     minus(stepKeys, approvalOnly, []string{"deployment", "uses", "with"}),
		"approval": minus(stepKeys, runOnly, loopKeys, []string{"uses", "with"}),
		"uses":     sortedSet(useKeys),
		"image":    sortedSet(imageKeys),
		"deploy":   sortedSet(deployKeys),
		"rollback": sortedSet(rollbackKeys),
		"rollout":  sortedSet(rolloutStepKeys),
		"ai":       sortedSet(aiKeys),
		"verify":   sortedSet(verifyKeys),
	}
}

func TestReferenceBasePipelinesParse(t *testing.T) {
	if _, err := Parse([]byte(probePipeline)); err != nil {
		t.Fatalf("the probe pipeline has problems, so probes would not reach every mapping:\n%v", err)
	}
	_, ps := readTemplate([]byte(probeTemplate), "check", nil, nil, 1, func(_, _ int, path, msg string) { t.Errorf("with.%s: %s", path, msg) },
		DefaultMaxLoopMembers, "ci/")
	if len(ps) > 0 {
		t.Fatalf("the probe template has problems:\n%v", ps)
	}
}

// The step-level probes check the assumption kindKeys rests on: that these
// lists are what the parser checks a step's keys against.
func TestReferenceStepListsAreTheParsers(t *testing.T) {
	for _, c := range []struct {
		path []any
		want []string
	}{
		{[]any{"steps", "build"}, stepKeys},
		{[]any{"steps", "approve"}, stepKeys},
		{[]any{"steps", "image"}, imageKeys},
		{[]any{"steps", "deploy"}, deployKeys},
		{[]any{"steps", "rollback"}, rollbackKeys},
		{[]any{"steps", "promote"}, rolloutStepKeys},
		// The parser lists the loop keys among the expected ones and then
		// refuses them with a reason of their own, so they are not documented.
		{[]any{"steps", "notes"}, sortedSet(aiKeys, loopKeys)},
		{[]any{"steps", "verify"}, sortedSet(verifyKeys, loopKeys)},
	} {
		got := accepted(t, probe{table: "kind", path: c.path})
		if !slices.Equal(got, sortedSet(c.want)) {
			t.Errorf("%v: the parser accepts %v; the list used for the reference is %v", c.path, got, sortedSet(c.want))
		}
	}
}

func TestReferencePipelineKeys(t *testing.T) {
	tables := readKeyTables(t, pipelineReference)
	known := map[string]bool{"step-common": true}
	for _, k := range stepKinds {
		known["kind-"+k] = true
	}

	// Mappings, by probing.
	byTable := map[string][]string{}
	for _, pr := range probes {
		known[pr.table] = true
		got := accepted(t, pr)
		if prev, ok := byTable[pr.table]; ok && !slices.Equal(prev, got) {
			t.Errorf("table %s: probes disagree: %v and %v", pr.table, prev, got)
		}
		byTable[pr.table] = got
	}
	for table, want := range byTable {
		rows, ok := tables[table]
		if !ok {
			t.Errorf("%s has no table <!-- keys: %s --> (the parser accepts %s there)", pipelineReference, table, strings.Join(want, ", "))
			continue
		}
		compareKeys(t, table, keysOf(rows), want)
	}

	// Step kinds: step-common rows apply to the kinds their second column
	// names; kind-<kind> rows to that kind.
	kinds := kindKeys()
	common := tables["step-common"]
	if common == nil {
		t.Errorf("%s has no table <!-- keys: step-common -->", pipelineReference)
	}
	for _, kind := range stepKinds {
		var doc []string
		for _, r := range common {
			if len(r.cells) < 2 {
				t.Errorf("%s:%d: a step-common row needs a second column naming its kinds", pipelineReference, r.line)
				continue
			}
			for _, m := range codeSpanRe.FindAllStringSubmatch(r.cells[1], -1) {
				if !slices.Contains(stepKinds, m[1]) {
					t.Errorf("%s:%d: %q is not a kind (%s)", pipelineReference, r.line, m[1], strings.Join(stepKinds, ", "))
				}
				if m[1] == kind {
					doc = append(doc, r.key)
				}
			}
		}
		own, ok := tables["kind-"+kind]
		if !ok {
			t.Errorf("%s has no table <!-- keys: kind-%s -->", pipelineReference, kind)
		}
		for _, r := range own {
			if slices.Contains(doc, r.key) {
				t.Errorf("%s:%d: %s is in step-common for %s and in kind-%s too", pipelineReference, r.line, r.key, kind, kind)
			}
			doc = append(doc, r.key)
		}
		compareKeys(t, "kind "+kind, sortedSet(doc), kinds[kind])
	}

	for name := range tables {
		if !known[name] {
			t.Errorf("%s: <!-- keys: %s --> is not a table this test knows; add a probe for it", pipelineReference, name)
		}
	}

	// Every list of keys in the source fits within one documented table, so
	// that a mapping nobody probes yet is noticed.
	var documented [][]string
	for _, rows := range tables {
		documented = append(documented, keysOf(rows))
	}
	for _, k := range kinds {
		documented = append(documented, k)
	}
	documented = append(documented, sortedSet(stepKeys, []string{"if"}), sortedSet(imageKeys, []string{"if"}))
	for _, l := range sourceKeyLists(t) {
		fits := false
		for _, d := range documented {
			if len(minus(l.keys, d)) == 0 {
				fits = true
				break
			}
		}
		if !fits {
			t.Errorf("%s: the keys %v are not all in any one table of %s; document the mapping and add a probe for it in this test",
				l.where, l.keys, pipelineReference)
		}
	}
}

func compareKeys(t *testing.T, what string, doc, code []string) {
	t.Helper()
	if extra := minus(doc, code); len(extra) > 0 {
		t.Errorf("%s: %s documents %v, which the parser does not accept there", what, pipelineReference, extra)
	}
	if missing := minus(code, doc); len(missing) > 0 {
		t.Errorf("%s: the parser accepts %v, which %s does not document there", what, missing, pipelineReference)
	}
}

type keyList struct {
	where string
	keys  []string
}

// sourceKeyLists finds the lists of keys written in the package: []string
// literals given to eachKey, and package-level variables named ...Keys.
func sourceKeyLists(t *testing.T) []keyList {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	strs := func(e ast.Expr) ([]string, bool) {
		cl, ok := e.(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		if at, ok := cl.Type.(*ast.ArrayType); !ok || at.Len != nil {
			return nil, false
		} else if id, ok := at.Elt.(*ast.Ident); !ok || id.Name != "string" {
			return nil, false
		}
		var out []string
		for _, el := range cl.Elts {
			bl, ok := el.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return nil, false
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	var out []keyList
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := goparser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if !strings.HasSuffix(id.Name, "Keys") || i >= len(vs.Values) {
						continue
					}
					if keys, ok := strs(vs.Values[i]); ok {
						out = append(out, keyList{where: fset.Position(id.Pos()).String() + " (" + id.Name + ")", keys: sortedSet(keys)})
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "eachKey" {
				return true
			}
			if keys, ok := strs(call.Args[2]); ok {
				out = append(out, keyList{where: fset.Position(call.Pos()).String(), keys: sortedSet(keys)})
			}
			return true
		})
	}
	if len(out) < 20 {
		t.Fatalf("found only %d lists of keys in the source; the scan is broken", len(out))
	}
	return out
}

// ---- examples ----

var validateMarkerRe = regexp.MustCompile(`^<!-- (validate|validate-template) -->\s*$`)

type docExample struct {
	kind, text string
	line       int
}

// docExamples returns the yaml blocks of a page that a validate marker
// precedes.
func docExamples(t *testing.T, file string) []docExample {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	var out []docExample
	for i := 0; i < len(lines); i++ {
		m := validateMarkerRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || strings.TrimSpace(lines[j]) != "```yaml" {
			t.Errorf("%s:%d: <!-- %s --> is not followed by a ```yaml block", file, i+1, m[1])
			continue
		}
		var b strings.Builder
		k := j + 1
		for ; k < len(lines) && strings.TrimSpace(lines[k]) != "```"; k++ {
			b.WriteString(lines[k] + "\n")
		}
		out = append(out, docExample{kind: m[1], text: b.String(), line: j + 2})
		i = k
	}
	return out
}

func TestReferencePipelineExamples(t *testing.T) {
	examples := docExamples(t, pipelineReference)
	if len(examples) == 0 {
		t.Fatalf("%s has no examples marked <!-- validate -->", pipelineReference)
	}
	for _, ex := range examples {
		switch ex.kind {
		case "validate":
			if _, err := Parse([]byte(ex.text)); err != nil {
				t.Errorf("%s: the example that starts on line %d has problems (lines are the example's own):\n%v", pipelineReference, ex.line, err)
			}
		case "validate-template":
			var head struct {
				Template string `yaml:"template"`
			}
			if err := yaml.Unmarshal([]byte(ex.text), &head); err != nil || head.Template == "" {
				t.Errorf("%s: the template example on line %d has no template: name", pipelineReference, ex.line)
				continue
			}
			_, ps := readTemplate([]byte(ex.text), head.Template, nil, nil, 1, func(_, _ int, path, msg string) {
				t.Errorf("%s: the template example on line %d, used without values: %s: %s", pipelineReference, ex.line, path, msg)
			}, DefaultMaxLoopMembers, "ci/")
			if len(ps) > 0 {
				t.Errorf("%s: the template example that starts on line %d has problems (lines are the example's own):\n%v", pipelineReference, ex.line, ps)
			}
		}
	}
}
