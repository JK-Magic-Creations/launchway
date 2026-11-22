package forks

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestPath is where a template declares what can be filled in.
const ManifestPath = ".launchway/template.yaml"

// Limits of a manifest and of what substitution touches.
const (
	maxManifestBytes = 64 << 10
	maxVariables     = 20
	maxValueBytes    = 100
	maxPatternBytes  = 200
	maxGlobs         = 50
)

// Variable is one thing a person fills in when they use the template. The
// token {{name}} is replaced, literally, by the value, in the files and file
// names the manifest names; nothing else is ever replaced and nothing is
// evaluated.
type Variable struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Pattern is a regular expression (Go syntax) the whole value must match.
	Pattern string `yaml:"pattern" json:"pattern,omitempty"`
	Default string `yaml:"default" json:"default,omitempty"`
}

// Manifest is .launchway/template.yaml.
type Manifest struct {
	Variables []Variable `yaml:"variables" json:"variables"`
	// Contents are the files whose text is substituted, as patterns of paths
	// (** matches across folders); empty: every text file.
	Contents []string `yaml:"contents" json:"contents,omitempty"`
	// Names says whether file and folder names are substituted too.
	Names bool `yaml:"names" json:"names,omitempty"`
}

var varName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// ParseManifest reads and checks a manifest. An empty one is a template with
// no variables.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) > maxManifestBytes {
		return m, fmt.Errorf("the template manifest is %d KiB, more than the %d KiB allowed", len(data)>>10, maxManifestBytes>>10)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return m, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("the template manifest %s is not valid: %v", ManifestPath, err)
	}
	if len(m.Variables) > maxVariables {
		return m, fmt.Errorf("the template declares %d variables, more than the %d allowed", len(m.Variables), maxVariables)
	}
	if len(m.Contents) > maxGlobs {
		return m, fmt.Errorf("the template names %d content patterns, more than the %d allowed", len(m.Contents), maxGlobs)
	}
	seen := map[string]bool{}
	for i, v := range m.Variables {
		switch {
		case !varName.MatchString(v.Name):
			return m, fmt.Errorf("variable %d: the name %q must be 1 to 31 lower-case letters, digits and underscores, starting with a letter", i+1, v.Name)
		case seen[v.Name]:
			return m, fmt.Errorf("variable %s is declared twice", v.Name)
		case len(v.Pattern) > maxPatternBytes:
			return m, fmt.Errorf("variable %s: the pattern is longer than %d characters", v.Name, maxPatternBytes)
		case len(v.Description) > 300:
			return m, fmt.Errorf("variable %s: the description is longer than 300 characters", v.Name)
		}
		seen[v.Name] = true
		if v.Pattern != "" {
			if _, err := regexp.Compile("^(?:" + v.Pattern + ")$"); err != nil {
				return m, fmt.Errorf("variable %s: the pattern is not a valid regular expression: %v", v.Name, err)
			}
		}
		if v.Default != "" {
			if err := v.check(v.Default, m.Names); err != nil {
				return m, fmt.Errorf("variable %s: its default fails its own rules: %v", v.Name, err)
			}
		}
	}
	for _, g := range m.Contents {
		if _, err := globRE(g); err != nil {
			return m, err
		}
	}
	return m, nil
}

// check validates one value: the variable's pattern, and the rules that hold
// whatever a template says. A value that goes into a file name (when the
// template substitutes names) must also be a plain name.
func (v Variable) check(value string, names bool) error {
	if len(value) > maxValueBytes {
		return fmt.Errorf("a value is at most %d bytes", maxValueBytes)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("a value cannot contain control characters or line breaks")
		}
	}
	if v.Pattern != "" && !regexp.MustCompile("^(?:"+v.Pattern+")$").MatchString(value) {
		return fmt.Errorf("%q does not match what %s must look like (%s)", value, v.Name, v.Pattern)
	}
	if names {
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\") || strings.Contains(value, "..") {
			return fmt.Errorf("%q cannot be used in a file name here (no slashes, no dots-only, no \"..\")", value)
		}
	}
	return nil
}

// globRE turns a path pattern into a regular expression: ** matches anything
// (and "**/" zero or more folders), * anything but a slash, ? one character
// but a slash.
func globRE(g string) (*regexp.Regexp, error) {
	if g == "" || len(g) > 200 || strings.ContainsAny(g, "\x00\\\\") || strings.HasPrefix(g, "/") {
		return nil, fmt.Errorf("%q is not a path pattern", g)
	}
	return globOptionalPrefix(g)
}

func globOptionalPrefix(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
