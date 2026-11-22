package executor

// The sandbox of a custom step (see internal/stepkit). A step that came from
// a custom step asks for nothing more than its manifest says, and an executor
// gives it that and no more where it can, and says so where it cannot:
//
//	                    podman / docker        kubernetes (per-step pod)   host
//	no network          --network none         pod label net=none, which   cannot: refused
//	                    (enforced)             the chart's policy keys on
//	listed hosts        NOT enforced: the      NOT enforced by pod: the    refused
//	                    step gets the network  chart policy for label
//	                    the engine gives it    net=hosts is the operator's
//	read-only root      --read-only, private   readOnlyRootFilesystem,     cannot
//	                    HOME in a tmpfs        private HOME and /tmp
//	writable paths      the workspace is ro,   NOT enforced: the           cannot
//	                    listed paths rw        workspace is one volume
//	capabilities        dropped, no new privs  dropped, no new privs       -
//
// The host executor runs the step as a process of the Station, so it enforces
// none of it, and refuses a custom step unless the operator allows steps
// that are not sandboxed.

import (
	"fmt"
	"sort"
	"strings"
)

// Sandbox is what a custom step may do. Nil: an ordinary step.
type Sandbox struct {
	// Name is the step's name, for messages and labels.
	Name string
	// Network lists the hosts the step may reach; empty: none.
	Network []string
	// Write lists workspace paths the step may write ("." is all of it).
	Write []string
	// IODir is the contract directory, relative to the workspace: always writable.
	IODir string
}

// NoNetwork reports whether the step may reach nothing.
func (sb *Sandbox) NoNetwork() bool { return sb != nil && len(sb.Network) == 0 }

// Label is the value of the pod label the chart's network policies key on.
func (sb *Sandbox) Label() string {
	if sb.NoNetwork() {
		return "none"
	}
	return "hosts"
}

// Describe is one line for the step's log.
func (sb *Sandbox) Describe() string {
	var net string
	if sb.NoNetwork() {
		net = "no network"
	} else {
		h := append([]string(nil), sb.Network...)
		sort.Strings(h)
		net = "network for " + strings.Join(h, ", ")
	}
	w := "workspace read-only"
	if len(sb.Write) > 0 {
		w = "writes " + strings.Join(sb.Write, ", ")
	}
	return fmt.Sprintf("Sandbox: %s, %s, read-only root filesystem, only the secrets it names", net, w)
}

// ErrUnsandboxed is why the host executor refuses a custom step.
func errHostCannotSandbox(name string) error {
	return fmt.Errorf("the custom step %s asks to run in a sandbox (no network unless it lists hosts, a read-only workspace, only the secrets it names), and the host executor runs steps as plain processes of the Station, which cannot enforce that. "+
		"Run it on a runner with the podman, docker or kubernetes executor, or let this runner run steps that are not sandboxed (the runner's allow_unsandboxed_steps setting)", name)
}
