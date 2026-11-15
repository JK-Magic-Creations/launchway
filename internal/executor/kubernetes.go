package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// KubernetesOptions configure the Kubernetes executor.
type KubernetesOptions struct {
	Kubeconfig       string            `yaml:"kubeconfig"`      // default: the usual rules, or in-cluster
	Context          string            `yaml:"context"`         // default: the current context
	Namespace        string            `yaml:"namespace"`       // must exist
	HelperImage      string            `yaml:"helper_image"`    // holds the workspace; needs sh and tar
	ServiceAccount   string            `yaml:"service_account"` // default: none mounted
	NodeSelector     map[string]string `yaml:"node_selector"`
	CPU              string            `yaml:"cpu"`          // request per container
	Memory           string            `yaml:"memory"`       // request per container
	MemoryLimit      string            `yaml:"memory_limit"` // optional limit per container
	ImagePullSecrets []string          `yaml:"image_pull_secrets"`
	KeepFailed       string            `yaml:"keep_failed"`   // how long to keep a failed run's pod for a resume; default 2h
	StartTimeout     string            `yaml:"start_timeout"` // how long to wait for images to pull; default 10m
}

// Kubernetes runs each run in one pod: a helper container that holds the
// workspace, and one long-lived container per image the pipeline uses.
// Steps are executed inside the container for their image, so steps of the
// same run share files and start instantly once the pod is up.
type Kubernetes struct {
	o          KubernetesOptions
	cfg        *rest.Config
	client     kubernetes.Interface
	owner      string // identifies this runner's pods
	keepFailed time.Duration
	startWait  time.Duration

	mu   sync.Mutex
	pods map[string]*runPod
}

type runPod struct {
	mu         sync.Mutex
	name       string
	workspace  string            // the Spec.WorkspaceID the pod holds
	containers map[string]string // image → container name
	ready      bool
}

const (
	labelManaged = "app.kubernetes.io/managed-by"
	labelRun     = "launchway.dev/run"
	labelOwner   = "launchway.dev/runner"
	annoImages   = "launchway.dev/images"
	annoReady    = "launchway.dev/workspace" // the WorkspaceID, once the workspace is copied in
	helperName   = "workspace"
)

// ErrInfrastructure marks a failure of the machinery rather than the step,
// such as a pod disappearing. The step should be run again, not failed.
var ErrInfrastructure = errors.New("infrastructure problem")

func infra(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInfrastructure, fmt.Sprintf(format, args...))
}

// NewKubernetes connects using a kubeconfig, or the in-cluster account when
// running inside a pod.
func NewKubernetes(o KubernetesOptions, runnerID string) (*Kubernetes, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.Kubeconfig != "" {
		rules.ExplicitPath = o.Kubeconfig
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: o.Context})
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("connecting to Kubernetes: %w", err)
	}
	if o.Namespace == "" {
		if ns, _, err := cc.Namespace(); err == nil && ns != "" {
			o.Namespace = ns
		} else {
			o.Namespace = "default"
		}
	}
	if o.HelperImage == "" {
		o.HelperImage = "docker.io/library/busybox:1.37"
	}
	if o.CPU == "" {
		o.CPU = "50m"
	}
	if o.Memory == "" {
		o.Memory = "64Mi"
	}
	k := &Kubernetes{o: o, cfg: cfg, pods: map[string]*runPod{}, keepFailed: 2 * time.Hour, startWait: 10 * time.Minute}
	for _, v := range []struct {
		s string
		d *time.Duration
	}{{o.KeepFailed, &k.keepFailed}, {o.StartTimeout, &k.startWait}} {
		if v.s != "" {
			d, err := time.ParseDuration(v.s)
			if err != nil {
				return nil, fmt.Errorf("kubernetes: %q is not a duration", v.s)
			}
			*v.d = d
		}
	}
	for _, q := range []string{o.CPU, o.Memory, o.MemoryLimit} {
		if q != "" {
			if _, err := resource.ParseQuantity(q); err != nil {
				return nil, fmt.Errorf("kubernetes: %q is not a valid quantity", q)
			}
		}
	}
	if k.client, err = kubernetes.NewForConfig(cfg); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(runnerID))
	k.owner = hex.EncodeToString(sum[:])[:10]
	return k, nil
}

func (k *Kubernetes) Name() string { return "kubernetes (" + k.o.Namespace + ")" }

// WorkspacePath is /workspace, where the pod's copy of the workspace is.
func (k *Kubernetes) WorkspacePath(string) string { return ContainerWorkspace }

// Check verifies the namespace is reachable, so mistakes show at start-up.
func (k *Kubernetes) Check(ctx context.Context) error {
	_, err := k.client.CoreV1().Pods(k.o.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("kubernetes: cannot list pods in namespace %q: %w", k.o.Namespace, err)
	}
	return nil
}

func (k *Kubernetes) Run(ctx context.Context, s Spec, out Sink) (int, error) {
	if s.RunID == "" {
		return -1, errors.New("kubernetes: the step has no run")
	}
	s = withDefaultContainer(s)
	rp, err := k.ensurePod(ctx, s, out)
	if err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, err
	}
	container := rp.containers[s.Container]
	if container == "" {
		k.forget(s.RunID)
		return -1, infra("the run's pod has no container called %s", s.Container)
	}

	key := containerName(s.Name)
	dir := "/launchway/" + key
	var env strings.Builder
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		fmt.Fprintf(&env, "export %s=%s\n", name, shellQuote(s.Env[name]))
	}
	fmt.Fprintf(&env, "export LAUNCHWAY_OUTPUTS=%s/outputs\n", dir)
	files := map[string][]byte{"script.sh": []byte(s.Script + "\n"), "env": []byte(env.String()), "outputs": nil}
	if err := k.upload(ctx, rp.name, dir, files); err != nil {
		return -1, k.podGone(ctx, s.RunID, rp, err)
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go pipeLines(stdoutR, "stdout", s.Mask, out, &wg)
	go pipeLines(stderrR, "stderr", s.Mask, out, &wg)

	streamCtx, stopStream := context.WithCancel(context.Background())
	defer stopStream()
	done := make(chan error, 1)
	script := fmt.Sprintf(`echo $$ > %[1]s/pid; cd /workspace; . %[1]s/env; exec sh -e %[1]s/script.sh`, dir)
	go func() {
		err := k.exec(streamCtx, rp.name, container, []string{"sh", "-c", script}, nil, stdoutW, stderrW)
		stdoutW.Close()
		stderrW.Close()
		wg.Wait()
		done <- err
	}()

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		k.signal(rp.name, container, dir, "TERM")
		select {
		case <-done:
		case <-time.After(stopGrace):
			k.signal(rp.name, container, dir, "KILL")
			stopStream()
			<-done
		}
		return -1, ctx.Err()
	}

	// Copy the outputs file back so the runner can read it as usual.
	var outputs bytes.Buffer
	_ = k.exec(ctx, rp.name, helperName, []string{"cat", dir + "/outputs"}, nil, &outputs, io.Discard)
	if err := os.MkdirAll(s.MetaDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(s.MetaDir, OutputsFile), outputs.Bytes(), 0o644)
	}
	_ = k.exec(context.Background(), rp.name, helperName, []string{"rm", "-rf", dir}, nil, io.Discard, io.Discard)

	if runErr == nil {
		return 0, nil
	}
	var ee utilexec.ExitError
	if errors.As(runErr, &ee) {
		return ee.ExitStatus(), nil
	}
	return -1, k.podGone(ctx, s.RunID, rp, runErr)
}

// podGone turns an error talking to the pod into an infrastructure error,
// forgetting the pod if it no longer exists so the next step recreates it.
func (k *Kubernetes) podGone(ctx context.Context, runID string, rp *runPod, err error) error {
	if _, gerr := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, rp.name, metav1.GetOptions{}); apierrors.IsNotFound(gerr) {
		k.forget(runID)
		return infra("the run's pod %s disappeared", rp.name)
	}
	return infra("talking to pod %s: %v", rp.name, err)
}

func (k *Kubernetes) forget(runID string) {
	k.mu.Lock()
	delete(k.pods, runID)
	k.mu.Unlock()
}

func (k *Kubernetes) signal(pod, container, dir, sig string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := fmt.Sprintf(`p=$(cat %s/pid 2>/dev/null) || exit 0; kill -%s -"$p" 2>/dev/null || kill -%s "$p" 2>/dev/null; true`, dir, sig, sig)
	_ = k.exec(ctx, pod, container, []string{"sh", "-c", cmd}, nil, io.Discard, io.Discard)
}

// ensurePod finds or creates the run's pod and fills its workspace.
func (k *Kubernetes) ensurePod(ctx context.Context, s Spec, out Sink) (*runPod, error) {
	k.mu.Lock()
	rp := k.pods[s.RunID]
	if rp == nil || rp.workspace != s.WorkspaceID {
		rp = &runPod{name: podName(s.RunID, k.owner), workspace: s.WorkspaceID}
		k.pods[s.RunID] = rp
	}
	k.mu.Unlock()
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if rp.ready {
		return rp, nil
	}
	sys := func(format string, a ...any) { out("system", fmt.Sprintf(format, a...)) }
	pods := k.client.CoreV1().Pods(k.o.Namespace)

	existing, err := pods.Get(ctx, rp.name, metav1.GetOptions{})
	switch {
	case err == nil && existing.DeletionTimestamp == nil && existing.Status.Phase == corev1.PodRunning && allReady(existing):
		var have map[string]string // container name → image
		_ = json.Unmarshal([]byte(existing.Annotations[annoImages]), &have)
		same := len(have) == len(s.Containers)
		for _, c := range s.Containers {
			if have[c.Name] != c.Image {
				same = false
			}
		}
		if same && existing.Annotations[annoReady] == s.WorkspaceID {
			rp.containers = map[string]string{}
			for _, c := range s.Containers {
				rp.containers[c.Name] = c.Name
			}
			rp.ready = true
			sys("Using the run's pod %s/%s", k.o.Namespace, rp.name)
			return rp, nil
		}
		fallthrough
	case err == nil:
		sys("Replacing the run's old pod")
		if err := k.deleteAndWait(ctx, rp.name); err != nil {
			return nil, err
		}
	case !apierrors.IsNotFound(err):
		return nil, infra("looking up pod %s: %v", rp.name, err)
	}

	pod, containers := k.podSpec(rp.name, s.RunID, s.Containers)
	var names []string
	for _, c := range s.Containers {
		kind := ""
		if c.Service {
			kind = " (service)"
		}
		names = append(names, c.Name+kind)
	}
	sys("Starting pod %s/%s: %s", k.o.Namespace, rp.name, strings.Join(names, ", "))
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("creating pod %s: %w", rp.name, err)
	}
	if err := k.waitRunning(ctx, rp.name, s.Containers, sys); err != nil {
		_ = pods.Delete(context.Background(), rp.name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))})
		return nil, err
	}
	sys("Copying the workspace into the pod")
	if err := k.uploadDir(ctx, rp.name, s.Workspace); err != nil {
		return nil, infra("copying the workspace: %v", err)
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annoReady, s.WorkspaceID)
	if _, err := pods.Patch(ctx, rp.name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return nil, infra("marking the pod ready: %v", err)
	}
	rp.containers, rp.ready = containers, true
	return rp, nil
}

func (k *Kubernetes) resources(cpu, memory string) corev1.ResourceRequirements {
	res := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	if cpu == "" {
		cpu = k.o.CPU
	}
	res.Requests[corev1.ResourceCPU] = resource.MustParse(cpu)
	switch {
	case memory != "": // asked for explicitly: guarantee it, and cap at it
		res.Requests[corev1.ResourceMemory] = resource.MustParse(memory)
		res.Limits[corev1.ResourceMemory] = resource.MustParse(memory)
	default:
		res.Requests[corev1.ResourceMemory] = resource.MustParse(k.o.Memory)
		if k.o.MemoryLimit != "" {
			res.Limits[corev1.ResourceMemory] = resource.MustParse(k.o.MemoryLimit)
		}
	}
	return res
}

func (k *Kubernetes) podSpec(name, runID string, specs []ContainerSpec) (*corev1.Pod, map[string]string) {
	keepAlive := []string{"sh", "-c", "trap 'exit 0' TERM INT; while true; do sleep 3600 & wait $!; done"}
	mounts := []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "launchway", MountPath: "/launchway"}}
	containers := map[string]string{}
	images := map[string]string{}
	cs := []corev1.Container{{Name: helperName, Image: k.o.HelperImage, Command: keepAlive, WorkingDir: "/workspace",
		VolumeMounts: mounts, Resources: k.resources("", "")}}
	for _, sp := range specs {
		images[sp.Name] = sp.Image
		c := corev1.Container{Name: sp.Name, Image: sp.Image, WorkingDir: "/workspace", VolumeMounts: mounts,
			Resources: k.resources(sp.CPU, sp.Memory), ImagePullPolicy: corev1.PullIfNotPresent}
		if sp.Service {
			c.Command = sp.Command // nil: the image's own entrypoint
			c.WorkingDir = ""
			keys := make([]string, 0, len(sp.Env))
			for key := range sp.Env {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				c.Env = append(c.Env, corev1.EnvVar{Name: key, Value: sp.Env[key]})
			}
		} else {
			c.Command = keepAlive
			containers[sp.Name] = sp.Name
		}
		cs = append(cs, c)
	}
	imgJSON, _ := json.Marshal(images)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      map[string]string{labelManaged: "launchway", labelRun: runID, labelOwner: k.owner},
			Annotations: map[string]string{annoImages: string(imgJSON)},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: ptr(int64(5)),
			AutomountServiceAccountToken:  ptr(k.o.ServiceAccount != ""),
			ServiceAccountName:            k.o.ServiceAccount,
			NodeSelector:                  k.o.NodeSelector,
			Containers:                    cs,
			Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "launchway", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
			},
		},
	}
	for _, s := range k.o.ImagePullSecrets {
		pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: s})
	}
	return pod, containers
}

// waitRunning waits for every container to start, explaining what it is
// waiting for, and gives up with a clear reason if an image cannot be used.
func (k *Kubernetes) waitRunning(ctx context.Context, name string, specs []ContainerSpec, sys func(string, ...any)) error {
	service := map[string]bool{}
	for _, c := range specs {
		service[c.Name] = c.Service
	}
	deadline := time.Now().Add(k.startWait)
	lastNote := ""
	for {
		pod, err := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return infra("waiting for pod %s: %v", name, err)
		}
		if pod.Status.Phase == corev1.PodRunning && allReady(pod) {
			return nil
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("the pod stopped: %s", podProblem(pod))
		}
		for _, st := range pod.Status.ContainerStatuses {
			if w := st.State.Waiting; w != nil {
				switch w.Reason {
				case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull":
					return fmt.Errorf("cannot pull %s: %s", st.Image, oneLine(w.Message))
				case "CreateContainerConfigError", "CreateContainerError":
					return fmt.Errorf("cannot start a container for %s: %s", st.Image, oneLine(w.Message))
				}
			}
			if t := st.State.Terminated; t != nil {
				if service[st.Name] {
					return fmt.Errorf("the service %s stopped (exit %d): %s", st.Name, t.ExitCode, k.tail(ctx, name, st.Name))
				}
				return fmt.Errorf("the container %s (%s) stopped at once (exit %d). Its image needs /bin/sh", st.Name, st.Image, t.ExitCode)
			}
		}
		if note := podProblem(pod); note != lastNote {
			sys("Waiting for the pod: %s", note)
			lastNote = note
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the pod did not start within %s: %s", k.startWait, podProblem(pod))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// tail returns the last lines a container printed, for explaining failures.
func (k *Kubernetes) tail(ctx context.Context, pod, container string) string {
	n := int64(15)
	b, err := k.client.CoreV1().Pods(k.o.Namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: &n}).DoRaw(ctx)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return "no output"
	}
	return strings.TrimSpace(string(b))
}

// withDefaultContainer fills in a single container for callers that only
// give an image.
func withDefaultContainer(s Spec) Spec {
	if len(s.Containers) == 0 {
		s.Containers = []ContainerSpec{{Name: "image-1", Image: s.Image}}
		s.Container = "image-1"
	}
	return s
}

func allReady(p *corev1.Pod) bool {
	if len(p.Status.ContainerStatuses) != len(p.Spec.Containers) {
		return false
	}
	for _, st := range p.Status.ContainerStatuses {
		if st.State.Running == nil {
			return false
		}
	}
	return true
}

func podProblem(p *corev1.Pod) string {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue && c.Message != "" {
			return "not scheduled yet: " + oneLine(c.Message)
		}
	}
	var waiting []string
	for _, st := range p.Status.ContainerStatuses {
		if w := st.State.Waiting; w != nil {
			waiting = append(waiting, fmt.Sprintf("%s %s", st.Image, strings.ToLower(w.Reason)))
		}
	}
	if len(waiting) > 0 {
		return strings.Join(waiting, ", ")
	}
	return strings.ToLower(string(p.Status.Phase))
}

func (k *Kubernetes) deleteAndWait(ctx context.Context, name string) error {
	pods := k.client.CoreV1().Pods(k.o.Namespace)
	if err := pods.Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))}); err != nil && !apierrors.IsNotFound(err) {
		return infra("deleting old pod %s: %v", name, err)
	}
	for i := 0; i < 120; i++ {
		if _, err := pods.Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return infra("old pod %s did not go away", name)
}

// exec runs a command in a container, streaming its input and output.
func (k *Kubernetes) exec(ctx context.Context, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := k.client.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(k.o.Namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: container, Command: cmd,
			Stdin: stdin != nil, Stdout: stdout != nil, Stderr: stderr != nil}, scheme.ParameterCodec)
	spdy, err := remotecommand.NewSPDYExecutor(k.cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	ws, err := remotecommand.NewWebSocketExecutor(k.cfg, "GET", req.URL().String())
	if err != nil {
		return err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	return ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

// upload writes small files into dir in the pod.
func (k *Kubernetes) upload(ctx context.Context, pod, dir string, files map[string][]byte) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o666, Size: int64(len(files[n])), ModTime: time.Now()}); err != nil {
			return err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd := fmt.Sprintf("mkdir -p %s && tar xf - -C %s && chmod -R a+rw %s", dir, dir, dir)
	if err := k.exec(ctx, pod, helperName, []string{"sh", "-c", cmd}, &buf, io.Discard, &stderr); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// uploadDir copies a local directory into the pod's workspace.
func (k *Kubernetes) uploadDir(ctx context.Context, pod, dir string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTar(pw, dir)) }()
	var stderr bytes.Buffer
	err := k.exec(ctx, pod, helperName,
		[]string{"sh", "-c", "tar xf - -C /workspace && chmod -R a+rwX /workspace"}, pr, io.Discard, &stderr)
	pr.Close()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func writeTar(w io.Writer, root string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		case info.Mode().IsDir(), info.Mode().IsRegular():
		default:
			return nil // sockets, devices and pipes are not copied
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Cleanup deletes the step pods in the namespace that no longer need to
// exist (see removable), whichever runner made them: a runner that was
// replaced, for example by a Deployment, leaves its pods to the others.
func (k *Kubernetes) Cleanup(ctx context.Context, lookup RunLookup) error {
	sel := fmt.Sprintf("%s=launchway,%s", labelManaged, labelRun)
	list, err := k.client.CoreV1().Pods(k.o.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil || len(list.Items) == 0 {
		return err
	}
	ids := make([]string, 0, len(list.Items))
	for _, p := range list.Items {
		ids = append(ids, p.Labels[labelRun])
	}
	infos, err := lookup(ctx, ids)
	if err != nil {
		return err
	}
	for _, p := range list.Items {
		id := p.Labels[labelRun]
		info, known := infos[id]
		mine := p.Labels[labelOwner] == k.owner
		if !removable(info, known, mine, k.keepFailed) {
			continue
		}
		if err := k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if mine {
			k.forget(id)
		}
	}
	return nil
}

func podName(runID, owner string) string {
	n := "launchway-" + strings.ToLower(strings.ReplaceAll(runID, "_", "-")) + "-" + owner
	if len(n) > 63 {
		n = n[:63]
	}
	return strings.TrimRight(n, "-")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func ptr[T any](v T) *T { return &v }
