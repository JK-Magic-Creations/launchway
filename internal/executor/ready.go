package executor

// Waiting for services to be ready (schema 27): on Kubernetes a readiness
// probe on the service's container, on Podman a health check; the first step
// starts only when every service is ready, or fails as an infrastructure
// problem saying which check failed and what the service printed last.

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Ready says when a service is ready. Exactly one of Run, TCP or HTTPPort is
// set (see pipeline.Ready).
type Ready struct {
	Run       string // a command run in the service's container
	TCP       int
	HTTPPort  int
	HTTPPath  string
	Every     time.Duration
	Within    time.Duration
	Described string // what is waited for, for the log
}

func (r *Ready) every() time.Duration {
	if r.Every < time.Second {
		return 2 * time.Second
	}
	return r.Every
}

// retries is how many checks fit in the time allowed.
func (r *Ready) retries() int {
	n := int(r.Within / r.every())
	if n < 1 {
		n = 1
	}
	return n
}

// clamp32 is n as an int32, within the range (ports and counts are far
// below it; the pipeline's parser bounds them).
func clamp32(n int) int32 {
	return int32(max(math.MinInt32, min(n, math.MaxInt32))) //nolint:gosec // clamped above
}

func seconds(d time.Duration) int32 { return clamp32(int(d / time.Second)) }

// readinessProbe is the Kubernetes probe for a service's check: the period is
// how often to check, and the failure threshold how many checks fit in the
// time allowed.
func readinessProbe(r *Ready) *corev1.Probe {
	if r == nil {
		return nil
	}
	p := &corev1.Probe{
		PeriodSeconds:    seconds(r.every()),
		FailureThreshold: clamp32(r.retries()),
		TimeoutSeconds:   max(1, min(seconds(r.every()), 10)),
		SuccessThreshold: 1,
	}
	switch {
	case r.Run != "":
		p.Exec = &corev1.ExecAction{Command: []string{"sh", "-c", r.Run}}
	case r.TCP > 0:
		p.TCPSocket = &corev1.TCPSocketAction{Port: intstr.FromInt32(clamp32(r.TCP))}
	default:
		p.HTTPGet = &corev1.HTTPGetAction{Path: r.HTTPPath, Port: intstr.FromInt32(clamp32(r.HTTPPort))}
	}
	return p
}

// healthCommand is the command a Podman health check runs inside the
// service's container; it needs sh, and for tcp nc or bash, for http wget or
// curl, which the service's image may not have (the failure says so).
func healthCommand(r *Ready) string {
	switch {
	case r.Run != "":
		return r.Run
	case r.TCP > 0:
		return fmt.Sprintf(`(command -v nc >/dev/null 2>&1 && nc -z 127.0.0.1 %[1]d) || (command -v bash >/dev/null 2>&1 && bash -c 'exec 3<>/dev/tcp/127.0.0.1/%[1]d')`, r.TCP)
	}
	url := "http://127.0.0.1:" + strconv.Itoa(r.HTTPPort) + r.HTTPPath
	return fmt.Sprintf(`(command -v wget >/dev/null 2>&1 && wget -q -O /dev/null %[1]s) || (command -v curl >/dev/null 2>&1 && curl -fsS -o /dev/null %[1]s)`, url)
}

// podmanHealthArgs are the arguments of podman run that give a service its
// health check.
func podmanHealthArgs(r *Ready) []string {
	if r == nil {
		return nil
	}
	return []string{"--health-cmd", "sh -c " + shellQuote(healthCommand(r)),
		"--health-interval", r.every().String(), "--health-retries", strconv.Itoa(r.retries()), "--health-timeout", "10s"}
}

// waitHealthy waits until a Podman service's health check passes, saying so
// in the step log, or fails with the check and the tail of the service's
// output once the time allowed has passed.
func (c *Container) waitHealthy(ctx context.Context, name, service string, r *Ready, out Sink) error {
	start := time.Now()
	desc := nonEmpty(r.Described, "its check")
	out("system", fmt.Sprintf("Waiting for service %s... (%s)", service, desc))
	for {
		b, _ := exec.CommandContext(ctx, c.Bin, "inspect", "-f", "{{.State.Health.Status}}|{{.State.Running}}", name).Output() //nolint:gosec // the runner's own container binary and a container name it made
		status, running, _ := strings.Cut(strings.TrimSpace(string(b)), "|")
		switch {
		case status == "healthy":
			out("system", fmt.Sprintf("Service %s ready after %d s", service, int(time.Since(start).Seconds())))
			shipServiceTail(out, service, c.logTail(ctx, name))
			return nil
		case running == "false":
			shipServiceTail(out, service, c.logTail(ctx, name))
			return cannotStart("the service %s stopped before it was ready: %s", service, c.logTail(ctx, name))
		case time.Since(start) >= r.Within:
			shipServiceTail(out, service, c.logTail(ctx, name))
			return cannotStart("the service %s was not ready within %s (waiting for %s): %s", service, r.Within, desc, c.logTail(ctx, name))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(r.every(), time.Second)):
		}
	}
}

// logTail is the last lines a container printed.
func (c *Container) logTail(ctx context.Context, name string) string {
	b, err := exec.CommandContext(ctx, c.Bin, "logs", "--tail", "15", name).CombinedOutput() //nolint:gosec // the runner's own container binary and a container name it made
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "no output"
	}
	return strings.TrimSpace(string(b))
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// unreadyServices lists the services of a running pod that have a readiness
// probe and are not ready yet.
func unreadyServices(p *corev1.Pod) []string {
	probed := map[string]bool{}
	for _, c := range p.Spec.Containers {
		probed[c.Name] = c.ReadinessProbe != nil
	}
	var out []string
	for _, st := range p.Status.ContainerStatuses {
		if probed[st.Name] && !st.Ready {
			out = append(out, st.Name)
		}
	}
	return out
}

// shipServiceTail sends the last lines a service printed to the step's log as
// the stream service, each line starting with the service's name, so that
// they can be read with launchway runs logs --service. The runner masks them
// like any output, and the tail is short (a service's output is not followed
// beyond it).
func shipServiceTail(out Sink, service, tail string) {
	if tail == "no output" {
		return
	}
	for _, line := range strings.Split(tail, "\n") {
		out("service", service+": "+line)
	}
}
