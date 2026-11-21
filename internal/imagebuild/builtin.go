package imagebuild

import (
	"maps"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func secretRefOf(store, key string) pipeline.SecretRef {
	return pipeline.SecretRef{Store: store, Key: key}
}

// The registry that is part of Launchway needs no configuration on a runner:
// the server hands an image step its address and a credential made for the
// run, and the step uses it under the name BuiltinName. The credential is
// never in the runner's files; it reaches the builder only through the
// build's session, like any registry credential, and only for this registry.

// BuiltinName is the name pipelines give the built-in registry.
const BuiltinName = "launchway"

// BuiltinStore is the secret store the runner answers itself for the
// built-in registry's credential: no stored password exists anywhere.
const BuiltinStore = "launchway-run"

// Builtin is what the server tells a runner about the built-in registry for
// one step.
type Builtin struct {
	Host     string
	Insecure bool
	Username string
	// Push says the run may push (not a pull request or draft run).
	Push bool
}

// WithBuiltin returns a configuration that also has the built-in registry
// for a project's step: pulls with the run's credential and, when it may
// push, pushes to the project's repositories and no others. A registry the
// operator configured under the same name is kept as it is.
func (c *Config) WithBuiltin(b Builtin, project string) *Config {
	out := &Config{}
	if c != nil {
		*out = *c
	}
	regs := map[string]RegistryConfig{}
	if c != nil {
		maps.Copy(regs, c.Registries)
	}
	if _, ok := regs[BuiltinName]; ok {
		out.Registries = regs
		return out
	}
	cred := &Credentials{Username: b.Username, Password: secretRefOf(BuiltinStore, "registry")}
	rc := RegistryConfig{Host: b.Host, Insecure: b.Insecure, Credentials: nil, PullCredentials: cred}
	if b.Push {
		rc.Credentials = cred
		rc.Push = []PushRule{{Projects: []string{project}, Repositories: []string{project, project + "/**"}}}
	}
	regs[BuiltinName] = rc
	out.Registries = regs
	return out
}
