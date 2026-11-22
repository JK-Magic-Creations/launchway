package executor

import (
	"fmt"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/stepkit"
)

// containerSandbox is what a custom step adds to the container's command.
type containerSandbox struct {
	args     []string
	wsMode   string // ":ro" when the workspace is read-only
	env      []string
	envValue map[string]string
}

// sandboxArgs makes the Podman or Docker flags a custom step's sandbox
// needs. Everything it can enforce it enforces: no network unless hosts are
// listed, a read-only root filesystem with a private HOME and /tmp, the
// workspace read-only except the paths the manifest lists (and the contract
// directory), no capabilities and no new privileges. A list of hosts is not
// enforced: Podman cannot filter by name, so the step gets the engine's own
// network and the log says so.
func (c *Container) sandboxArgs(s Spec, out Sink) (containerSandbox, error) {
	sb := s.Sandbox
	if sb == nil {
		return containerSandbox{}, nil
	}
	if len(s.services()) > 0 {
		return containerSandbox{}, cannotStart("the custom step %s runs in a sandbox of its own, which cannot share a pod with the run's services; give it no sandbox-breaking dependency, or run the services as steps of their own", sb.Name)
	}
	cs := containerSandbox{wsMode: ":ro", envValue: map[string]string{"HOME": "/home/launchway"}, env: []string{"HOME"}}
	cs.args = []string{
		"--read-only",
		"--tmpfs", "/tmp:rw,mode=1777,size=512m",
		"--tmpfs", "/home/launchway:rw,mode=0700,size=256m",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "2048",
	}
	if sb.NoNetwork() {
		cs.args = append(cs.args, "--network", "none")
	} else {
		out("system", "The step lists the hosts "+strings.Join(sb.Network, ", ")+"; this executor cannot filter by host, so the step has the container engine's network and the list is only recorded")
	}
	dirs := append([]string{sb.IODir}, sb.Write...)
	for _, d := range dirs {
		if d == "." {
			cs.wsMode = ""
			continue
		}
		host, err := stepkit.SafeJoin(s.Workspace, d)
		if err != nil {
			return containerSandbox{}, cannotStart("the custom step %s lists %q as writable: %v", sb.Name, d, err)
		}
		if err := os.MkdirAll(host, 0o777); err != nil {
			return containerSandbox{}, cannotStart("the custom step %s: preparing %s: %v", sb.Name, d, err)
		}
		_ = os.Chmod(host, 0o777)
		// Checked again once it exists: a path that turned into a link is refused.
		if _, err := stepkit.SafeJoin(s.Workspace, d); err != nil {
			return containerSandbox{}, cannotStart("the custom step %s lists %q as writable: %v", sb.Name, d, err)
		}
		cs.args = append(cs.args, "-v", fmt.Sprintf("%s:/workspace/%s", host, d))
	}
	out("system", sb.Describe())
	return cs, nil
}

// withEnv returns env with the values added, where it does not set them itself.
func withEnv(env, add map[string]string) map[string]string {
	out := make(map[string]string, len(env)+len(add))
	for k, v := range add {
		out[k] = v
	}
	for k, v := range env {
		out[k] = v
	}
	return out
}
