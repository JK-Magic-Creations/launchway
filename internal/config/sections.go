package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Since schema 23 most of what this file describes is kept in the database
// (internal/settings). These helpers let that package read the same
// sections with the same rules.

// ParseSections reads configuration text as a configuration file is read
// (unknown keys refused), without defaults, paths or checks: what launchway
// config import reads. Keys named in extra are taken out first and returned
// as they were (an export's objects:, which only import reads).
func ParseSections(data []byte, extra ...string) (*Config, map[string]*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	taken := map[string]*yaml.Node{}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		m := doc.Content[0]
		kept := m.Content[:0:0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			key := m.Content[i].Value
			if contains(extra, key) {
				taken[key] = m.Content[i+1]
				continue
			}
			kept = append(kept, m.Content[i], m.Content[i+1])
		}
		m.Content = kept
	}
	var buf bytes.Buffer
	if doc.Kind != 0 {
		if err := yaml.NewEncoder(&buf).Encode(&doc); err != nil {
			return nil, nil, err
		}
	}
	c := &Config{}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	for i := range c.Projects {
		if err := c.Projects[i].Normalize(); err != nil {
			return nil, nil, err
		}
	}
	return c, taken, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Normalize takes a pipeline's on written unquoted (which YAML 1.1 tools
// turn into true) for what it was meant to be, as the file does.
func (p *ProjectConfig) Normalize() error {
	if pp := p.Pipeline; pp != nil && pp.OnAsTrue != nil {
		if pp.On.Push != nil || pp.On.PullRequest.Kind != 0 {
			return fmt.Errorf("Flight %s: pipeline has both `on` and `true` (an unquoted on); keep one", p.Name)
		}
		pp.On, pp.OnAsTrue = *pp.OnAsTrue, nil
	}
	return nil
}

// SetAccessInDatabase says whether role bindings come from the database (so
// an empty access section is not a mistake by itself).
func (a *AuthConfig) SetAccessInDatabase(v bool) { a.accessInDatabase = v }

// File is the path the configuration was read from; "" if none.
func (c *Config) File() string { return c.file }

// Check reports what is wrong with the auth section, all of it, the way
// the server will when it starts.
func (a *AuthConfig) Check() error { return a.check() }
