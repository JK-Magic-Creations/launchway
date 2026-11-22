package stepkit

import (
	"fmt"
	"os"
	"path/filepath"
)

// IO is the directory a step's contract files are in for one attempt.
type IO struct {
	Rel    string // relative to the workspace
	Host   string // on this machine
	Inside string // the workspace as the step sees it
}

// Env is the contract's variables for the step, with the paths as the step
// sees them.
func (io *IO) Env(name string, inputTypes, values map[string]string) map[string]string {
	in := io.Inside + "/" + io.Rel
	env := map[string]string{
		EnvContract:  fmt.Sprint(ContractVersion),
		EnvName:      name,
		EnvInputs:    in + "/" + InputsFile,
		EnvOutputs:   in + "/" + OutputsFile,
		EnvResults:   in + "/" + ResultsFile,
		EnvArtifacts: in + "/" + ArtifactsDir,
		EnvReports:   in + "/" + ReportsDir,
	}
	for _, k := range SortedKeys(values) {
		if t := inputTypes[k]; t == TypeString || t == TypeNumber || t == TypeBoolean {
			env[EnvWithPrefix+EnvPart(k)] = values[k]
		}
	}
	return env
}

// PrepareIO makes the directory a step attempt writes to, fresh, below the
// workspace, and writes its inputs. The path is checked part by part, so a
// link in the workspace cannot lead the step's files elsewhere. secretEnv
// names, for each secret input, the variable its value is in.
func PrepareIO(workspace, rel, inside, name string, inputTypes, values, secretEnv map[string]string) (*IO, error) {
	host, err := SafeJoin(workspace, rel)
	if err != nil {
		return nil, fmt.Errorf("preparing the step's directory: %w", err)
	}
	if err := os.RemoveAll(host); err != nil {
		return nil, err
	}
	for _, d := range []string{"", ArtifactsDir, ReportsDir} {
		p := filepath.Join(host, d)
		if err := os.MkdirAll(p, 0o777); err != nil {
			return nil, err
		}
		_ = os.Chmod(p, 0o777)
	}
	m := &Manifest{Name: name}
	for _, k := range SortedKeys(inputTypes) {
		m.Inputs = append(m.Inputs, &Input{Name: k, Type: inputTypes[k]})
	}
	doc, err := m.BuildInputs(values, secretEnv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(host, InputsFile), doc, 0o644); err != nil {
		return nil, err
	}
	for _, f := range []string{OutputsFile, ResultsFile} {
		p := filepath.Join(host, f)
		if err := os.WriteFile(p, nil, 0o666); err != nil {
			return nil, err
		}
		_ = os.Chmod(p, 0o666)
	}
	return &IO{Rel: rel, Host: host, Inside: inside}, nil
}
