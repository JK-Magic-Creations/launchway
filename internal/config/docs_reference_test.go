package config

// Drift checks between the configuration code and its reference pages:
//
//   - docs/reference/server-config.md documents launchway.yaml (Config),
//   - docs/reference/runner-config.md documents `launchway runner --config`
//     (RunnerFile), and
//   - docs/reference/helm-values.md documents the chart's values.yaml and
//     values.schema.json.
//
// Each page lists settings in tables marked with an HTML comment on the
// line before the table, <!-- keys: server -->, <!-- keys: runner --> or
// <!-- keys: helm -->. Each row starts with the setting's full path in a
// code span: `auth.oidc.issuer`. A list's items are written [] and a map's
// entries <name>: `projects[].webhooks.<name>.token_env`.
//
// The settings a file may hold are found by reflection over the yaml tags
// of the structs the loaders decode into (both refuse unknown keys), so a
// new field fails this test until it is documented. Every leaf setting (a
// value, a list of values, or a map of values) must have a row; rows for the
// objects that hold them are allowed but not required; a row naming a path
// the code does not have fails. The server's runner section is the runner
// file's settings, documented once in runner-config.md: a server path
// runner.X is found there as X.
//
// For Helm, every key in values.yaml and every property in
// values.schema.json must have a row, except inside the values the chart
// hands to Kubernetes as they are (helmOpaque), which are documented as one
// row each.
//
// Examples: a ```yaml block that <!-- validate-config server --> or
// <!-- validate-config runner --> precedes is loaded with Load or
// LoadRunner, and one that <!-- validate-helm --> precedes is rendered with
// helm template (skipped without helm).

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	serverReference = "../../docs/reference/server-config.md"
	runnerReference = "../../docs/reference/runner-config.md"
	helmReference   = "../../docs/reference/helm-values.md"
	chartDir        = "../../deploy/helm/launchway"
)

var (
	keysMarkerRe = regexp.MustCompile(`^<!-- keys: ([a-z0-9_.:-]+) -->\s*$`)
	rowKeyRe     = regexp.MustCompile("^\\|\\s*`([^`]+)`")
)

// docKeys returns the keys in the first column of the tables marked with
// name, with the line of each.
func docKeys(t *testing.T, file, name string) map[string]int {
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
	out := map[string]int{}
	tables := 0
	for i := 0; i < len(lines); i++ {
		m := keysMarkerRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		if m[1] != name {
			t.Errorf("%s:%d: <!-- keys: %s --> is not a table this page's test knows (want %s)", file, i+1, m[1], name)
			continue
		}
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j+1 >= len(lines) || !strings.HasPrefix(lines[j], "|") || !strings.HasPrefix(lines[j+1], "|") {
			t.Errorf("%s:%d: <!-- keys: %s --> is not followed by a table", file, i+1, name)
			continue
		}
		tables++
		for j += 2; j < len(lines) && strings.HasPrefix(lines[j], "|"); j++ {
			k := rowKeyRe.FindStringSubmatch(lines[j])
			if k == nil {
				t.Errorf("%s:%d: a row of a %s table does not start with a setting in a code span", file, j+1, name)
				continue
			}
			if prev, dup := out[k[1]]; dup {
				t.Errorf("%s:%d: %s is listed twice (also on line %d)", file, j+1, k[1], prev)
			}
			out[k[1]] = j + 1
		}
	}
	if tables == 0 {
		t.Fatalf("%s has no tables marked <!-- keys: %s -->", file, name)
	}
	return out
}

var yamlNodeType = reflect.TypeOf(yaml.Node{})

// settingPaths lists the paths a YAML file decoded into t may hold: leaves
// are values, lists of values and maps of values; all adds the objects that
// hold them.
func settingPaths(t reflect.Type) (leaves, all map[string]bool) {
	leaves, all = map[string]bool{}, map[string]bool{}
	var walk func(t reflect.Type, prefix string)
	join := func(prefix, name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "." + name
	}
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		leaf := func() {
			leaves[prefix] = true
			all[prefix] = true
		}
		switch {
		case t == yamlNodeType:
			leaf()
		case t.Kind() == reflect.Struct:
			if prefix != "" {
				all[prefix] = true
			}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if !f.IsExported() {
					continue
				}
				name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
				switch {
				case name == "-":
					continue
				case strings.Contains(opts, "inline"):
					walk(f.Type, prefix)
					continue
				case name == "":
					name = strings.ToLower(f.Name) // yaml.v3's default
				}
				p := join(prefix, name)
				if name == "true" {
					// ProjectPipelineConfig.OnAsTrue: the spelling YAML 1.1
					// tools give an unquoted on. Documented as one row.
					leaves[p], all[p] = true, true
					continue
				}
				walk(f.Type, p)
			}
		case t.Kind() == reflect.Slice && isObject(t.Elem()):
			all[prefix] = true
			walk(t.Elem(), prefix+"[]")
		case t.Kind() == reflect.Map && isObject(t.Elem()):
			all[prefix] = true
			walk(t.Elem(), prefix+".<name>")
		default:
			leaf()
		}
	}
	walk(t, "")
	return leaves, all
}

func isObject(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t != yamlNodeType && (t.Kind() == reflect.Struct || t.Kind() == reflect.Map)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestReferenceServerConfig(t *testing.T) {
	leaves, all := settingPaths(reflect.TypeOf(Config{}))
	runnerLeaves, _ := settingPaths(reflect.TypeOf(RunnerFile{}))
	doc := docKeys(t, serverReference, "server")
	runnerDoc := docKeys(t, runnerReference, "runner")
	for _, p := range sortedKeys(leaves) {
		if _, ok := doc[p]; ok {
			continue
		}
		if rest, ok := strings.CutPrefix(p, "runner."); ok {
			if _, ok := runnerDoc[rest]; ok && runnerLeaves[rest] {
				continue
			}
			t.Errorf("the server setting %s is documented in neither %s nor (as %s) %s", p, serverReference, rest, runnerReference)
			continue
		}
		t.Errorf("the server setting %s is not documented in %s", p, serverReference)
	}
	for _, p := range sortedKeys(doc) {
		if !all[p] {
			t.Errorf("%s:%d: %s is not a setting of launchway.yaml", serverReference, doc[p], p)
		}
	}
}

func TestReferenceRunnerConfig(t *testing.T) {
	leaves, all := settingPaths(reflect.TypeOf(RunnerFile{}))
	doc := docKeys(t, runnerReference, "runner")
	for _, p := range sortedKeys(leaves) {
		if _, ok := doc[p]; !ok {
			t.Errorf("the runner setting %s is not documented in %s", p, runnerReference)
		}
	}
	for _, p := range sortedKeys(doc) {
		if !all[p] {
			t.Errorf("%s:%d: %s is not a setting of a runner's --config file", runnerReference, doc[p], p)
		}
	}
}

// helmOpaque are values the chart passes to Kubernetes as they are; their
// insides are Kubernetes' business and are documented as one row.
var helmOpaque = regexp.MustCompile(`(^|\.)(resources|startupProbe|livenessProbe|readinessProbe|namespaceLabels)$|^(podSecurityContext|securityContext)$`)

// helmPaths lists the paths of values.yaml and of values.schema.json's
// properties: required (leaves, and opaque values) and all (with the
// objects that hold them).
func helmPaths(t *testing.T) (required, all map[string]bool) {
	t.Helper()
	required, all = map[string]bool{}, map[string]bool{}
	data, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(n *yaml.Node, prefix string)
	walk = func(n *yaml.Node, prefix string) {
		if prefix != "" {
			all[prefix] = true
		}
		if n.Kind != yaml.MappingNode || len(n.Content) == 0 || helmOpaque.MatchString(prefix) {
			required[prefix] = true
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if prefix != "" {
				p = prefix + "." + p
			}
			walk(n.Content[i+1], p)
		}
	}
	walk(doc.Content[0], "")
	data, err = os.ReadFile(filepath.Join(chartDir, "values.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := yaml.Unmarshal(data, &schema); err != nil { // JSON is YAML
		t.Fatal(err)
	}
	var walkSchema func(s map[string]any, prefix string)
	walkSchema = func(s map[string]any, prefix string) {
		if prefix != "" {
			all[prefix] = true
		}
		props, _ := s["properties"].(map[string]any)
		if len(props) == 0 || helmOpaque.MatchString(prefix) {
			if prefix != "" {
				required[prefix] = true
			}
			return
		}
		for k, v := range props {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if sub, ok := v.(map[string]any); ok {
				walkSchema(sub, p)
			}
		}
	}
	walkSchema(schema, "")
	// An object whose keys values.yaml writes out is documented by its
	// keys; the schema may know it only as an object (a $ref).
	for p := range required {
		for q := range required {
			if strings.HasPrefix(q, p+".") {
				delete(required, p)
				break
			}
		}
	}
	return required, all
}

func TestReferenceHelmValues(t *testing.T) {
	required, all := helmPaths(t)
	doc := docKeys(t, helmReference, "helm")
	for _, p := range sortedKeys(required) {
		if _, ok := doc[p]; !ok {
			t.Errorf("the chart value %s is not documented in %s", p, helmReference)
		}
	}
	for _, p := range sortedKeys(doc) {
		if !all[p] {
			t.Errorf("%s:%d: %s is not a value of the chart (values.yaml or values.schema.json)", helmReference, doc[p], p)
		}
	}
}

// ---- examples ----

var exampleMarkerRe = regexp.MustCompile(`^<!-- (validate-config server|validate-config runner|validate-helm) -->\s*$`)

type example struct {
	kind, text string
	line       int
}

func examples(t *testing.T, file string) []example {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	var out []example
	for i := 0; i < len(lines); i++ {
		m := exampleMarkerRe.FindStringSubmatch(lines[i])
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
		out = append(out, example{kind: m[1], text: b.String(), line: j + 2})
		i = k
	}
	return out
}

func TestReferenceConfigExamples(t *testing.T) {
	n := 0
	for _, file := range []string{serverReference, runnerReference, helmReference} {
		for _, ex := range examples(t, file) {
			n++
			switch ex.kind {
			case "validate-config server", "validate-config runner":
				// Values that the examples read from the environment.
				t.Setenv("LAUNCHWAY_OIDC_SECRET", "example")
				if err := loadFile(t, ex.text, ex.kind == "validate-config runner"); err != nil {
					t.Errorf("%s: the example on line %d does not load: %v", file, ex.line, err)
				}
			case "validate-helm":
				if _, err := exec.LookPath("helm"); err != nil {
					t.Logf("%s: the example on line %d needs helm; skipped", file, ex.line)
					continue
				}
				vf := filepath.Join(t.TempDir(), "values.yaml")
				if err := os.WriteFile(vf, []byte(ex.text), 0o644); err != nil {
					t.Fatal(err)
				}
				out, err := exec.Command("helm", "template", "t", chartDir, "--set", "externalURL=https://launchway.example.com", "--set", "config.extraConfig.auth.mode=none", "-f", vf).CombinedOutput()
				if err != nil {
					t.Errorf("%s: helm template fails with the example on line %d: %v\n%s", file, ex.line, err, out)
					continue
				}
				// Every configuration file the chart renders must load.
				for name, f := range configMapFiles(t, string(out)) {
					if err := loadFile(t, f, name != "server"); err != nil {
						t.Errorf("%s: with the example on line %d, the chart's %s configuration does not load: %v", file, ex.line, name, err)
					}
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("the configuration reference pages have no examples marked for validation")
	}
}

// configMapFiles returns the files of the ConfigMaps in rendered manifests,
// by ConfigMap name suffix (server, runner, deploy-runner).
func configMapFiles(t *testing.T, manifests string) map[string]string {
	t.Helper()
	files := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(manifests))
	for {
		var doc struct {
			Kind     string
			Metadata struct{ Name string }
			Data     map[string]string
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc.Kind == "ConfigMap" {
			for _, v := range doc.Data {
				files[strings.TrimPrefix(doc.Metadata.Name, "t-launchway-")] = v
			}
		}
	}
	return files
}
