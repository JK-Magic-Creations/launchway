package plan

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// FilePath is where a repository keeps its own update settings. It is read
// from the default branch only: a branch cannot loosen the settings.
const FilePath = ".launchway/updates.yaml"

// FileVersion is the only version of the file format.
const FileVersion = 1

type fileDoc struct {
	Version int `yaml:"version"`
	Partial `yaml:",inline"`
}

// ParseFile reads .launchway/updates.yaml. Unknown keys are an error naming
// the key, so a misspelled setting is never silently dropped.
func ParseFile(data []byte) (Layer, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d fileDoc
	if err := dec.Decode(&d); err != nil && err != io.EOF {
		return Layer{}, fmt.Errorf("%s is not valid: %w", FilePath, err)
	}
	if d.Version != FileVersion {
		return Layer{}, fmt.Errorf("%s must start with \"version: 1\"", FilePath)
	}
	if len(d.Partial.Lock) > 0 {
		return Layer{}, fmt.Errorf("%s cannot lock settings: only an administrator level can", FilePath)
	}
	return Layer{Level: "file", Partial: d.Partial}, nil
}
