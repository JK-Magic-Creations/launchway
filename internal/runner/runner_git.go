package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
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

// outsideClone is the settings that carry a run's credential for a repository
// kept on another Git host to the origin of its address only, as environment
// for the one git process (never in the workspace's git configuration).
func outsideClone(a *engine.OutsideAuth) (string, []string) {
	u, err := url.Parse(a.URL)
	if err != nil || u.Host == "" {
		return a.URL, nil
	}
	origin := u.Scheme + "://" + u.Host
	n, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	var env []string
	for i := 0; i < n; i++ {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, os.Getenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i))),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, os.Getenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i))))
	}
	basic := base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Secret))
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1),
		fmt.Sprintf("GIT_CONFIG_KEY_%d=http.%s/.extraHeader", n, origin),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=Authorization: Basic %s", n, basic))
	return a.URL, env
}
