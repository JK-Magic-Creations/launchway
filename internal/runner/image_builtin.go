package runner

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// builtinRegistry gives an image step the built-in registry: its address and a
// credential made for this run, both from the server and neither stored on the
// runner. It returns the configuration to build with (the runner's own, plus
// the registry, for this step only) and the credential, which is masked in the
// step's output. A step the server gave no registry gets the runner's
// configuration as it is.
func (r *Runner) builtinRegistry(job *engine.Job, mask *imageMasker, sys func(string, ...any)) (*imagebuild.Config, string) {
	reg := job.ImageBuild.Registry
	if reg == nil {
		return r.Images, ""
	}
	mask.add("the built-in registry credential", reg.Token)
	sys("Uses the built-in registry at %s", reg.Host)
	return r.Images.WithBuiltin(imagebuild.Builtin{Host: reg.Host, Insecure: reg.Insecure, Username: reg.Username, Push: reg.Push}, job.Project), reg.Token
}

// imageLookup reads the secrets an image step names: the built-in registry's
// credential is the run's own, anything else comes from the runner's stores.
func (r *Runner) imageLookup(job *engine.Job, mask *imageMasker, sys func(string, ...any), builtinToken string) func(context.Context, pipeline.SecretRef) (string, error) {
	return func(ctx context.Context, ref pipeline.SecretRef) (string, error) {
		if ref.Store == imagebuild.BuiltinStore && builtinToken != "" {
			return builtinToken, nil
		}
		v, err := r.lookup(ctx, job.Project, ref, sys)
		if err == nil {
			mask.add("credential "+ref.String(), v)
		}
		return v, err
	}
}
