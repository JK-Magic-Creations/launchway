package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// stateStore is the reserved secret store whose values the server makes for
// the step: a credential for one workspace of the project's built-in
// Terraform/OpenTofu state, valid for this run only.
//
//	secrets:
//	  TF_HTTP_PASSWORD: launchway:state#prod
//
// The step also gets TF_HTTP_ADDRESS, TF_HTTP_LOCK_ADDRESS,
// TF_HTTP_UNLOCK_ADDRESS and TF_HTTP_USERNAME, so a repository needs no
// backend settings and no bucket. The key is always "state"; the field is
// the workspace (default "default").
const stateStore = "launchway"

func (r *Runner) stateCredential(ctx context.Context, job *engine.Job, ref pipeline.SecretRef) (string, error) {
	if ref.Key != "state" {
		return "", fmt.Errorf("the launchway store has the key state only, not %q", ref.Key)
	}
	var out struct {
		Token string `json:"token"`
		Path  string `json:"path"`
	}
	if _, err := r.Client.call(ctx, "config/state-credential", map[string]string{"lease_id": job.LeaseID, "workspace": ref.Field}, &out); err != nil {
		return "", fmt.Errorf("asking the server for a state credential: %w", err)
	}
	if out.Token == "" || out.Path == "" {
		return "", fmt.Errorf("the server gave no state credential")
	}
	base := strings.TrimSuffix(r.Client.Server, "/") + out.Path
	if job.Env == nil {
		job.Env = map[string]string{}
	}
	job.Env["TF_HTTP_ADDRESS"] = base
	job.Env["TF_HTTP_LOCK_ADDRESS"] = base + "/lock"
	job.Env["TF_HTTP_UNLOCK_ADDRESS"] = base + "/lock"
	job.Env["TF_HTTP_LOCK_METHOD"] = "LOCK"
	job.Env["TF_HTTP_UNLOCK_METHOD"] = "UNLOCK"
	job.Env["TF_HTTP_USERNAME"] = "launchway"
	return out.Token, nil
}
