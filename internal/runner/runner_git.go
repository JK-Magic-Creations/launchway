package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// git runs git through the one hardened runner. The repositories a runner
// clones belong to other people, so their objects are checked on transfer.
func git(ctx context.Context, dir string, args ...string) error {
	return gitEnv(ctx, dir, nil, args...)
}

// hostedClone is the address to clone a repository hosted by the server from,
// and the git settings that carry the run's credential to that address only
// (as a header scoped to the server's address, added to any GIT_CONFIG
// settings the runner's own environment already has).
func (r *Runner) hostedClone(path, credential string) (string, []string) {
	base := strings.TrimRight(r.Client.Server, "/")
	n, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	var env []string
	for i := 0; i < n; i++ {
		k, v := os.Getenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i)), os.Getenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i))
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, v))
	}
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=http.%s/.extraHeader", n, base),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=Authorization: Bearer %s", n, credential))
	return base + "/git/" + path + ".git", env
}

func gitEnv(ctx context.Context, dir string, env []string, args ...string) error {
	_, err := gitexec.Run(ctx, gitexec.Spec{Dir: dir, Args: args, Env: env, Inherit: gitexec.Remote, Untrusted: true})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("git %s: %s", args[0], redact.URLs(ee.Output()))
		}
		return fmt.Errorf("git %s: %s", args[0], redact.URLs(err.Error()))
	}
	return nil
}
