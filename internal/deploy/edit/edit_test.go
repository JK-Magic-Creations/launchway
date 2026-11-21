package edit

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestImage(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		image   string
		tag     string
		want    string // whole expected output
		matches int
	}{
		{
			name:  "deployment",
			image: "team/webapp", tag: "1.9.0",
			in: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: webapp   # the app
spec:
  template:
    spec:
      containers:
        - name: webapp
          image: team/webapp:1.8.1 # bumped by Launchway
          ports:
          -   containerPort: 8080
`,
			want: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: webapp   # the app
spec:
  template:
    spec:
      containers:
        - name: webapp
          image: team/webapp:1.9.0 # bumped by Launchway
          ports:
          -   containerPort: 8080
`,
			matches: 1,
		},
		{
			name:  "rollout with registry and sidecar",
			image: "team/webapp", tag: "2",
			in: `apiVersion: argoproj.io/v1alpha1
kind: Rollout
spec:
  template:
    spec:
      initContainers:
      - image: "harbor.example.com/team/webapp:1"
        name: migrate
      containers:
      - image: 'harbor.example.com/team/webapp:1'
        name: webapp
      - image: harbor.example.com/team/webapp-api:1
        name: api
      - image: harbor.example.com/other/team/webapp:1
        name: other
`,
			want: `apiVersion: argoproj.io/v1alpha1
kind: Rollout
spec:
  template:
    spec:
      initContainers:
      - image: "harbor.example.com/team/webapp:2"
        name: migrate
      containers:
      - image: 'harbor.example.com/team/webapp:2'
        name: webapp
      - image: harbor.example.com/team/webapp-api:1
        name: api
      - image: harbor.example.com/other/team/webapp:1
        name: other
`,
			matches: 2,
		},
		{
			name:  "no tag under image key gets one",
			image: "team/webapp", tag: "v3",
			in:      "image: team/webapp\nother: localhost:5000/team/webapp\nlist:\n- localhost:5000/team/webapp\n",
			want:    "image: team/webapp:v3\nother: localhost:5000/team/webapp\nlist:\n- localhost:5000/team/webapp\n",
			matches: 1,
		},
		{
			name:  "localhost registry with port",
			image: "team/webapp", tag: "v3",
			in:      "image: localhost:5000/team/webapp\nx: [localhost/team/webapp:1, xteam/webapp:1]\n",
			want:    "image: localhost:5000/team/webapp:v3\nx: [localhost/team/webapp:v3, xteam/webapp:1]\n",
			matches: 2,
		},
		{
			name:  "image with registry must match exactly",
			image: "registry.example:5000/team/app", tag: "7",
			in:      "a: registry.example:5000/team/app:6\nb: team/app:6\nc: other.example/team/app:6\n",
			want:    "a: registry.example:5000/team/app:7\nb: team/app:6\nc: other.example/team/app:6\n",
			matches: 1,
		},
		{
			name:  "already at tag is unchanged and counted",
			image: "team/webapp", tag: "1",
			in:      "image:   'team/webapp:1'   # same\n",
			want:    "image:   'team/webapp:1'   # same\n",
			matches: 1,
		},
		{
			name:  "several documents",
			image: "team/webapp", tag: "5",
			in: `# first
image: team/webapp:4
---
kind: Job
spec:
  containers:
  - {name: a, image: team/webapp:3}
...
---
- team/webapp:2
- "  team/webapp:1  "
`,
			want: `# first
image: team/webapp:5
---
kind: Job
spec:
  containers:
  - {name: a, image: team/webapp:5}
...
---
- team/webapp:5
- "  team/webapp:5  "
`,
			matches: 4,
		},
		{
			name:  "flow mapping",
			image: "team/webapp", tag: "9",
			in:      "c: {image: team/webapp:1}\n",
			want:    "c: {image: team/webapp:9}\n",
			matches: 1,
		},
		{
			name:  "crlf",
			image: "team/webapp", tag: "9",
			in:      "# c\r\na:\r\n  image: team/webapp:1\r\n  b: 'x'\r\n",
			want:    "# c\r\na:\r\n  image: team/webapp:9\r\n  b: 'x'\r\n",
			matches: 1,
		},
		{
			name:  "odd indentation, tabs in comments, unicode before value",
			image: "team/webapp", tag: "9",
			in:      "\xEF\xBB\xBFköln:     {ü: team/webapp:1, é: x}\t# ünïcode\n\n\n      # deep comment\nz:\n       - team/webapp:1\n",
			want:    "\xEF\xBB\xBFköln:     {ü: team/webapp:9, é: x}\t# ünïcode\n\n\n      # deep comment\nz:\n       - team/webapp:9\n",
			matches: 2,
		},
		{
			name:  "keys, block scalars and whole-document values are not looked at",
			image: "team/webapp", tag: "9",
			in:      "team/webapp:1: key\nscript: |\n  team/webapp:1\nimage: team/webapp:1\n---\nteam/webapp:1\n",
			want:    "team/webapp:1: key\nscript: |\n  team/webapp:1\nimage: team/webapp:9\n---\nteam/webapp:1\n",
			matches: 1,
		},
		{
			name:  "single quote and double quote escapes in other values are kept",
			image: "team/webapp", tag: "9",
			in:      "a: 'it''s'\nb: \"tab\\there\"\nimage: \"team/webapp:1\"\n",
			want:    "a: 'it''s'\nb: \"tab\\there\"\nimage: \"team/webapp:9\"\n",
			matches: 1,
		},
		{
			name:  "inside an anchored map, the alias follows",
			image: "team/webapp", tag: "9",
			in:      "base: &b\n  image: team/webapp:1\nuse: *b\n",
			want:    "base: &b\n  image: team/webapp:9\nuse: *b\n",
			matches: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, n, err := Image([]byte(c.in), c.image, c.tag)
			if err != nil {
				t.Fatalf("Image: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("output:\n%q\nwant:\n%q", out, c.want)
			}
			if n != c.matches {
				t.Errorf("matches = %d, want %d", n, c.matches)
			}
		})
	}
}

func TestImageErrors(t *testing.T) {
	cases := []struct {
		name, in, image, tag, want string
	}{
		{"helm values do not match", "image:\n  repository: team/webapp\n  tag: \"1\"\n", "team/webapp", "2", "no reference to image team/webapp was found"},
		{"zero matches", "image: other/app:1\n", "team/webapp", "2", "no reference to image team/webapp"},
		{"empty file", "", "team/webapp", "2", "no reference to image team/webapp"},
		{"digest", "a: 1\nb:\n  image: team/webapp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n", "team/webapp", "2",
			"line 3: image team/webapp is pinned by digest (@sha256:0123456789ab…); a tag cannot move it"},
		{"tag and digest", "x: team/webapp:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n", "team/webapp", "2", "line 1: image team/webapp is pinned by digest"},
		{"anchored value", "image: &i team/webapp:1\nb: *i\n", "team/webapp", "2", "line 1: the reference to image team/webapp has an anchor (&i)"},
		{"tagged value", "image: !!str team/webapp:1\n", "team/webapp", "2", "line 1: the reference to image team/webapp has an explicit tag"},
		{"bad yaml", "a: [\n", "team/webapp", "2", "the file is not valid YAML"},
		{"bad image", "a: 1\n", "team/webapp:1", "2", "has a tag"},
		{"bad tag", "a: 1\n", "team/webapp", "-x", "tag \"-x\" is not valid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _, err := Image([]byte(c.in), c.image, c.tag)
			if err == nil {
				t.Fatalf("no error; output:\n%s", out)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

func TestSet(t *testing.T) {
	cases := []struct {
		name, in, path, value, want string
	}{
		{
			name: "nested", path: "image.tag", value: "1.9.0",
			in:   "# values\nimage:\n  repository: team/webapp   # repo\n  tag: 1.8.1 # tag\nreplicas: 2\n",
			want: "# values\nimage:\n  repository: team/webapp   # repo\n  tag: 1.9.0 # tag\nreplicas: 2\n",
		},
		{
			name: "needs quotes as a number", path: "image.tag", value: "1.20",
			in:   "image:\n  tag: v1\n",
			want: "image:\n  tag: \"1.20\"\n",
		},
		{
			name: "needs quotes as a bool", path: "a.b", value: "true",
			in:   "a: {b: x, c: y}\n",
			want: "a: {b: \"true\", c: y}\n",
		},
		{
			name: "single quotes kept", path: "a", value: "it's",
			in:   "a: 'old'  # c\n",
			want: "a: 'it''s'  # c\n",
		},
		{
			name: "double quotes kept", path: "a", value: `say "hi"\`,
			in:   "a: \"old\"\n",
			want: "a: \"say \\\"hi\\\"\\\\\"\n",
		},
		{
			name: "plain with colon space is quoted", path: "a", value: "x: y",
			in:   "a: old\n",
			want: "a: \"x: y\"\n",
		},
		{
			name: "sequence index", path: "images[1]", value: "b:2",
			in:   "images:\n- a:1\n-   b:1   # second\n",
			want: "images:\n- a:1\n-   b:2   # second\n",
		},
		{
			name: "nested indexes", path: "m[0][1]", value: "z",
			in:   "m: [[a, b], [c]]\n",
			want: "m: [[a, z], [c]]\n",
		},
		{
			name: "multi-source application", path: "spec.sources[0].targetRevision", value: "v2.0.0",
			in: `apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  sources:
    - repoURL: https://charts.example.com
      chart: webapp
      targetRevision: v1.0.0
    - repoURL: https://git.example.com/values.git
      targetRevision: main
`,
			want: `apiVersion: argoproj.io/v1alpha1
kind: Application
spec:
  sources:
    - repoURL: https://charts.example.com
      chart: webapp
      targetRevision: v2.0.0
    - repoURL: https://git.example.com/values.git
      targetRevision: main
`,
		},
		{
			name: "one of several documents", path: "spec.sources[1].targetRevision", value: "abc123",
			in:   "kind: A\n---\nspec:\n  sources:\n  - x: 1\n  - targetRevision: main\n---\nkind: C\n",
			want: "kind: A\n---\nspec:\n  sources:\n  - x: 1\n  - targetRevision: abc123\n---\nkind: C\n",
		},
		{
			name: "crlf", path: "a.b", value: "new",
			in:   "a:\r\n  b: old\r\n  c: 1\r\n",
			want: "a:\r\n  b: new\r\n  c: 1\r\n",
		},
		{
			name: "null value", path: "a", value: "x",
			in:   "a: ~\nb: null\n",
			want: "a: x\nb: null\n",
		},
		{
			name: "empty value", path: "a", value: "x",
			in:   "a:   # nothing yet\nb: 1\n",
			want: "a: x   # nothing yet\nb: 1\n",
		},
		{
			name: "empty value at end", path: "a.b", value: "null",
			in:   "a:\n  b:\n",
			want: "a:\n  b: \"null\"\n",
		},
		{
			name: "quoted key form", path: `metadata.annotations["example.com/rev"]`, value: "abc",
			in:   "metadata:\n  annotations:\n    example.com/rev: old\n",
			want: "metadata:\n  annotations:\n    example.com/rev: abc\n",
		},
		{
			name: "same value is unchanged", path: "a", value: "x",
			in:   "a:    x  \n",
			want: "a:    x  \n",
		},
		{
			name: "empty string", path: "a", value: "",
			in:   "a: x\n",
			want: "a: \"\"\n",
		},
		{
			name: "unicode before value", path: `["ü"].b`, value: "neu",
			in:   "ü: {é: 1, b: alt}\n",
			want: "ü: {é: 1, b: neu}\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := Set([]byte(c.in), c.path, c.value)
			if err != nil {
				t.Fatalf("Set: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("output:\n%q\nwant:\n%q", out, c.want)
			}
		})
	}
}

func TestSetQuoting(t *testing.T) {
	// Each value set on a plain scalar must read back as the same string.
	for _, v := range []string{"1.20", "true", "0123", "null", "~", "1e3", "a: b", "a #b", "*x", "&x", "!x", "|x", ">x",
		"%x", "@x", "`x", "-", "- x", "-x", "---", "...", "x:", "[x]", "{x}", "a,b", "'x", "\"x", " x", "x ", "0x1F",
		".inf", "yes", "no", "on", "\tx", "é", "\u00a0x", "a\\b", "#x", "?x", ":x", "2026-11-15", "<<"} {
		out, err := Set([]byte("k: old\nf: {k: old}\n"), "k", v)
		if err != nil {
			t.Fatalf("Set %q: %v", v, err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(out, &m); err != nil {
			t.Fatalf("Set %q: output does not parse: %v\n%s", v, err, out)
		}
		if m["k"] != v {
			t.Errorf("Set %q: reads back as %#v\n%s", v, m["k"], out)
		}
		out, err = Set([]byte("k: old\nf: {k: old}\n"), "f.k", v)
		if err != nil {
			t.Fatalf("Set f.k %q: %v", v, err)
		}
		if err := yaml.Unmarshal(out, &m); err != nil {
			t.Fatalf("Set f.k %q: output does not parse: %v\n%s", v, err, out)
		}
		if got := m["f"].(map[string]any)["k"]; got != v {
			t.Errorf("Set f.k %q: reads back as %#v\n%s", v, got, out)
		}
	}
}

func TestSetErrors(t *testing.T) {
	cases := []struct {
		name, in, path, value, want string
	}{
		{"missing key", "a: 1\nimage:\n  repository: x\n", "image.tag", "1", `image.tag: there is no key "tag" under image (line 3)`},
		{"missing top key", "a: 1\n", "image.tag", "1", `image.tag: there is no key "image" at the top of the document (line 1)`},
		{"index out of range", "s:\n- a\n- b\n", "s[2]", "x", "s[2]: there is no item [2] under s, which has 2 items (line 2)"},
		{"not a list", "s: {a: 1}\n", "s[0]", "x", "s[0]: s is a map, not a list"},
		{"not a map", "s: [1]\n", "s.a", "x", `s.a: s is a list, not a map, so it has no key "a"`},
		{"scalar not a map", "s: 1\n", "s.a", "x", `s.a: s is a single value, not a map`},
		{"in two documents", "a: 1\n---\nb: 2\n---\na: 3\n", "a", "x", "a: the path is in 2 documents (lines 1, 5)"},
		{"in no documents", "a: 1\n---\nb: 2\n", "c", "x", `c: none of the 2 documents in the file has this path (document 1: there is no key "c" at the top of the document (line 1); document 2: there is no key "c"`},
		{"map target", "a:\n  b: 1\n", "a", "x", "a: this is a map, not a single value; give the path of a value inside it (line 2)"},
		{"list target", "a: [1]\n", "a", "x", "a: this is a list, not a single value"},
		{"block scalar", "a: |\n  text\n", "a", "x", "a: this is a block of text (| or >); Launchway edits single-line values only (line 1)"},
		{"folded scalar", "a: >-\n  text\n", "a", "x", "block of text"},
		{"multi-line plain", "a: one\n  two\n", "a", "x", "a: this value is written over several lines"},
		{"multi-line quoted", "a: \"one\n  two\"\n", "a", "x", "a: this value is written over several lines"},
		{"anchored target", "a: &x 1\nb: *x\n", "a", "x", "a: this has an anchor (&x)"},
		{"alias target", "a: &x 1\nb: *x\n", "b", "x", "b: this is an alias (*x)"},
		{"through anchor", "a: &x {b: 1}\nc: *x\n", "a.b", "x", "a.b: a has an anchor (&x)"},
		{"through alias", "a: &x {b: 1}\nc: *x\n", "c.b", "x", "c.b: c is an alias (*x)"},
		{"merge key", "base: &b {x: 1}\nm:\n  <<: *b\n", "m.x", "2", "keys brought in with << are not followed"},
		{"tagged target", "a: !!str 1\n", "a", "x", "a: this has an explicit tag"},
		{"newline in value", "a: 1\n", "a", "x\ny", "a: the value has a line break"},
		{"empty file", "", "a", "x", "a: the file is empty"},
		{"comment-only file", "# nothing\n", "a", "x", "a: the file is empty"},
		{"empty document", "---\n", "a", "x", "a: the document is empty"},
		{"bad path", "a: 1\n", "a.", "x", "it ends with a dot"},
		{"bad yaml", "a: b: c\n", "a", "x", "the file is not valid YAML: mapping values are not allowed"},
		{"empty sequence item", "a:\n- \n- b\n", "a[0]", "x", "a[0]: there is no value written here to replace (line"},
		{"root is a list", "- a\n", "a", "x", `a: the document is a list, not a map, so it has no key "a"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := Set([]byte(c.in), c.path, c.value)
			if err == nil {
				t.Fatalf("no error; output:\n%s", out)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

func TestCheckPath(t *testing.T) {
	good := []string{"a", "image.tag", "spec.sources[1].targetRevision", "images[0]", "m[0][12]", "a-b_c.D9",
		`metadata.annotations["example.com/x"]`, `["a.b"].c`, `a["b"]["c"][0]`}
	for _, p := range good {
		if err := CheckPath(p); err != nil {
			t.Errorf("CheckPath(%q) = %v, want nil", p, err)
		}
		pp, _ := parsePath(p)
		if pp.String() != p {
			t.Errorf("parsePath(%q).String() = %q", p, pp.String())
		}
	}
	bad := map[string]string{
		"":            "the path is empty",
		"a.":          "ends with a dot",
		".a":          "empty key at character 1",
		"a..b":        "empty key at character 3",
		"a[-1]":       "not an index",
		"a[x]":        "not an index",
		"a[]":         "needs an index",
		"a[0":         "not closed",
		"[0]":         "must start with a key",
		"a b":         `key "a b" may use only`,
		"a/b":         `key "a/b" may use only`,
		`a[""]`:       "empty key",
		`a["x\"y"]`:   "may not contain",
		"a[0]b":       "expected . or [",
		"a[99999999]": "too large",
		"a.[0]":       "empty key",
	}
	for p, want := range bad {
		err := CheckPath(p)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckPath(%q) = %v, want an error containing %q", p, err, want)
		}
	}
	if err := CheckPath(strings.Repeat("a", MaxPathLen+1)); err == nil {
		t.Error("CheckPath accepted an over-long path")
	}
}

func TestCheckImage(t *testing.T) {
	for _, s := range []string{"team/webapp", "registry.example:5000/team/app", "nginx", "localhost/app",
		"Harbor.Example.com/a/b", "a__b/c-d/e.f"} {
		if err := CheckImage(s); err != nil {
			t.Errorf("CheckImage(%q) = %v", s, err)
		}
	}
	bad := map[string]string{
		"":              "empty",
		"team/webapp:1": "has a tag",
		"team/Webapp":   "must be lower case",
		" team/webapp":  "spaces",
		"team//webapp":  "not a valid repository",
		"team/webapp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": "has a digest",
		"http://x/y":             "not a valid repository",
		"-a/b":                   "not a valid repository",
		strings.Repeat("a", 256): "not a valid repository",
	}
	for s, want := range bad {
		err := CheckImage(s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckImage(%q) = %v, want an error containing %q", s, err, want)
		}
	}
}

func TestCheckTag(t *testing.T) {
	for _, s := range []string{"1", "v1.2.3-rc.1", "_x", "latest", strings.Repeat("a", 128)} {
		if err := CheckTag(s); err != nil {
			t.Errorf("CheckTag(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", ".x", "-x", "a:b", "a/b", "a b", strings.Repeat("a", 129), "é"} {
		if err := CheckTag(s); err == nil {
			t.Errorf("CheckTag(%q) = nil, want an error", s)
		}
	}
}

// TestUnsupportedEncodings checks that files the offsets cannot be trusted
// for are refused rather than edited.
func TestUnsupportedEncodings(t *testing.T) {
	for _, in := range []string{"\xFF\xFEa\x00:\x00", "a: \xFF\n"} {
		if _, err := Set([]byte(in), "a", "x"); err == nil {
			t.Errorf("Set on %q: no error", in)
		}
	}
}

// TestErrorsAreNotWrapped makes sure callers see the message itself.
func TestErrorsAreNotWrapped(t *testing.T) {
	_, err := Set([]byte("a: 1\n"), "b", "x")
	if err == nil || errors.Unwrap(err) != nil {
		t.Errorf("error %v wraps another error", err)
	}
}

// lookup follows a path independently of find, for the fuzz test.
func lookup(data []byte, p path) []*yaml.Node {
	var hits []*yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if !errors.Is(err, io.EOF) {
				return nil
			}
			return hits
		}
		if len(doc.Content) == 0 {
			continue
		}
		n := doc.Content[0]
		for _, st := range p {
			var next *yaml.Node
			switch {
			case st.isIndex && n.Kind == yaml.SequenceNode && st.index < len(n.Content):
				next = n.Content[st.index]
			case !st.isIndex && n.Kind == yaml.MappingNode:
				for i := 0; i+1 < len(n.Content); i += 2 {
					if n.Content[i].Kind == yaml.ScalarNode && n.Content[i].Value == st.key {
						next = n.Content[i+1]
						break
					}
				}
			}
			n = next
			if n == nil {
				break
			}
		}
		if n != nil {
			hits = append(hits, n)
		}
	}
}

func FuzzSet(f *testing.F) {
	f.Add("image:\n  tag: v1 # c\n", "image.tag", "1.20")
	f.Add("a: [x, {b: 'y'}]\n---\nc: 1\n", "a[1].b", "it's")
	f.Add("a:\r\n  - \"q\"\r\n", "a[0]", "x\"y")
	f.Add("a: {b: c}\n", "a.b", "- x")
	f.Add("a:\n", "a", "true")
	f.Fuzz(func(t *testing.T, in, p, value string) {
		pp, err := parsePath(p)
		if err != nil {
			return
		}
		if _, err := readDocs([]byte(in)); err != nil {
			return
		}
		out, err := Set([]byte(in), p, value)
		if err != nil {
			return
		}
		hits := lookup(out, pp)
		if len(hits) != 1 {
			t.Fatalf("after Set, the path is in %d documents\ninput: %q\noutput: %q", len(hits), in, out)
		}
		if n := hits[0]; n.Kind != yaml.ScalarNode || n.Value != value || n.ShortTag() != "!!str" {
			t.Fatalf("after Set, the target reads %q (%s), want %q\ninput: %q\noutput: %q", n.Value, n.ShortTag(), value, in, out)
		}
	})
}

func TestImageDigest(t *testing.T) {
	d := "sha256:" + strings.Repeat("ab", 32)
	in := "# keep me\nspec:\n  containers:\n    - image: harbor.example.com/team/shop:1.2  # pinned below\n    - image: \"team/shop@sha256:" + strings.Repeat("cd", 32) + "\"\n    - image: team/other:1\n"
	out, n, err := ImageDigest([]byte(in), "team/shop", d)
	if err != nil || n != 2 {
		t.Fatalf("%v %d", err, n)
	}
	want := "# keep me\nspec:\n  containers:\n    - image: harbor.example.com/team/shop@" + d + "  # pinned below\n    - image: \"team/shop@" + d + "\"\n    - image: team/other:1\n"
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	// Again: nothing changes.
	again, _, err := ImageDigest(out, "team/shop", d)
	if err != nil || string(again) != string(out) {
		t.Fatalf("second edit: %v\n%s", err, again)
	}
	if _, _, err := ImageDigest([]byte(in), "team/shop", "sha256:short"); err == nil {
		t.Fatal("a short digest was taken")
	}
	// A tag cannot move a reference pinned by digest; the message says how.
	if _, _, err := Image(out, "team/shop", "v2"); err == nil || !strings.Contains(err.Error(), "digest: instead of tag:") {
		t.Fatalf("tag over a digest: %v", err)
	}
}

// A readable tag stays beside the digest, for references with a registry
// host and a tag, a bare name under image, and one already pinned.
func TestImageTagDigest(t *testing.T) {
	const d = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	in := "# shop\n" +
		"spec:\n  containers:\n" +
		"    - image: harbor.example.com/team/shop:ci-latest   # api\n" +
		"    - image: team/shop:20261101-aaaaaaaa@sha256:" + strings.Repeat("a", 64) + "\n" +
		"    - name: other\n      image: team/other:1\n" +
		"  env:\n    - { name: IMG, value: team/shop }\n"
	out, n, err := ImageTagDigest([]byte(in), "team/shop", "20261121-1234abcd", d)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v\n%s", n, err, out)
	}
	want := strings.NewReplacer(
		"harbor.example.com/team/shop:ci-latest", "harbor.example.com/team/shop:20261121-1234abcd@"+d,
		"team/shop:20261101-aaaaaaaa@sha256:"+strings.Repeat("a", 64), "team/shop:20261121-1234abcd@"+d,
	).Replace(in)
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if _, _, err := ImageTagDigest([]byte(in), "team/shop", "bad tag", d); err == nil {
		t.Fatal("a bad tag was accepted")
	}
	// Written again with the same values, nothing changes.
	again, _, err := ImageTagDigest(out, "team/shop", "20261121-1234abcd", d)
	if err != nil || string(again) != string(out) {
		t.Fatalf("not idempotent: %v", err)
	}
}
