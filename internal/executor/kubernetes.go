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
	"path"
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
	MaxCPU           string            `yaml:"max_cpu"`      // the most a container may ask for, checked before its pod is made
	MaxMemory        string            `yaml:"max_memory"`   // likewise; see checkResources
	ImagePullSecrets []string          `yaml:"image_pull_secrets"`
	KeepFailed       string            `yaml:"keep_failed"`   // a grace period before a failed or canceled run's pod is removed; default 0 (see removable)
	MaxPodAge        string            `yaml:"max_pod_age"`   // the longest any step pod may live, whatever the server says about its run; default 24h
	StartTimeout     string            `yaml:"start_timeout"` // how long to wait for images to pull; default 10m

	// Restricted, true unless set to false, makes step pods meet the Pod
	// Security Standard "restricted": every container, services included,
	// runs as RunAsUser and RunAsGroup instead of the image's own user, with
	// no privilege escalation, no capabilities and the RuntimeDefault seccomp
	// profile, and gets a writable home directory (see stepHome). Set it to
	// false to run images as their own user, root for many, as before; the
	// step namespace must then admit such pods (Pod Security "baseline").
	// Only the runner's operator decides this: nothing in a pipeline file
	// changes a pod's security context.
	Restricted *bool  `yaml:"restricted"`
	RunAsUser  *int64 `yaml:"run_as_user"`  // default 1000; never 0
	RunAsGroup *int64 `yaml:"run_as_group"` // default 1000; also owns the pod's volumes (fsGroup)
}

// restricted reports whether step pods get the restricted security context.
func (o KubernetesOptions) restricted() bool { return o.Restricted == nil || *o.Restricted }

// stepHome is the writable home directory of every container of a
// restricted step pod: an emptyDir, so that tools that keep files or caches
// under $HOME work for a user the image does not know.
const stepHome = "/home/launchway"

// homeEnv is set in every container of a restricted step pod, unless the
// container's own settings or the step set the variable. The caches point
// into stepHome so that they stay writable when a step moves HOME.
var homeEnv = []corev1.EnvVar{
	{Name: "HOME", Value: stepHome},
	{Name: "PIP_CACHE_DIR", Value: stepHome + "/.cache/pip"},
	{Name: "npm_config_cache", Value: stepHome + "/.npm"},
	{Name: "GOCACHE", Value: stepHome + "/.cache/go-build"},
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
	maxPodAge  time.Duration
	startWait  time.Duration

	mu    sync.Mutex
	pods  map[string]*runPod
	noted map[string]bool // what has been said once (see noteOnce)
	busy  map[string]int  // run → steps executing in this process now
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

// ErrCannotStart marks a failure to bring a step's containers up: an image
// that cannot be pulled, a pod that stopped or never started, a pod that
// could not be created. It is not the step's own failure, but trying again
// at once would not help, so the step is failed (as infrastructure) rather
// than lost.
var ErrCannotStart = errors.New("the step's containers could not be started")

type startError struct{ msg string }

func (e startError) Error() string        { return e.msg }
func (e startError) Is(target error) bool { return target == ErrCannotStart }

func cannotStart(format string, args ...any) error {
	return startError{fmt.Sprintf(format, args...)}
}

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
	if err := o.setDefaults(); err != nil {
		return nil, err
	}
	k := &Kubernetes{o: o, cfg: cfg, pods: map[string]*runPod{}, keepFailed: 0, maxPodAge: 24 * time.Hour, startWait: 10 * time.Minute}
	for _, v := range []struct {
		s string
		d *time.Duration
	}{{o.KeepFailed, &k.keepFailed}, {o.MaxPodAge, &k.maxPodAge}, {o.StartTimeout, &k.startWait}} {
		if v.s != "" {
			d, err := time.ParseDuration(v.s)
			if err != nil {
				return nil, fmt.Errorf("kubernetes: %q is not a duration", v.s)
			}
			*v.d = d
		}
	}
	for _, q := range []string{o.CPU, o.Memory, o.MemoryLimit, o.MaxCPU, o.MaxMemory} {
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

// setDefaults fills in the settings that were left out, and refuses
// security settings that would not mean what they say.
func (o *KubernetesOptions) setDefaults() error {
	if o.HelperImage == "" {
		o.HelperImage = "docker.io/library/busybox:1.37"
	}
	if o.CPU == "" {
		o.CPU = "50m"
	}
	if o.Memory == "" {
		o.Memory = "64Mi"
	}
	if !o.restricted() {
		if o.RunAsUser != nil || o.RunAsGroup != nil {
			return errors.New("kubernetes: run_as_user and run_as_group apply only when restricted is true; " +
				"with restricted: false, containers run as their image's user")
		}
		return nil
	}
	if o.RunAsUser != nil && *o.RunAsUser <= 0 {
		return fmt.Errorf("kubernetes: run_as_user %d is not allowed: it must be a user other than root (0); "+
			"set restricted: false to run images as their own user", *o.RunAsUser)
	}
	if o.RunAsGroup != nil && *o.RunAsGroup < 0 {
		return fmt.Errorf("kubernetes: run_as_group %d is not a group", *o.RunAsGroup)
	}
	if o.RunAsUser == nil {
		o.RunAsUser = ptr(int64(1000))
	}
	if o.RunAsGroup == nil {
		o.RunAsGroup = ptr(int64(1000))
	}
	return nil
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
	s = withDefaultContainer(s).withParallelism()
	defer k.holdRun(s.RunID)()
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
	script := stepScript(dir)
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

// stepScript is the command that runs a step whose files are in dir. The
// step's environment is exported from the env file, so it wins over the
// variables the container started with.
func stepScript(dir string) string {
	return fmt.Sprintf(`echo $$ > %[1]s/pid; cd /workspace; . %[1]s/env; exec sh -e %[1]s/script.sh`, dir)
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

	specs, err := k.checkResources(ctx, s.Containers, sys)
	if err != nil {
		return nil, err
	}
	s.Containers = specs
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
	for _, c := range s.Containers {
		if !c.Service && (c.CPU != "" || c.Memory != "" || c.CPULimit != "" || c.MemoryLimit != "") {
			sys("Container %s resources: %s", c.Name, c.describeResources())
		}
	}
	if k.o.restricted() {
		sys("Containers run as user %d (group %d), not as root, with HOME=%s: commands that need root, "+
			"such as installing packages with apt-get or apk, fail. The runner's operator can change this "+
			"(kubernetes.restricted)", *k.o.RunAsUser, *k.o.RunAsGroup, stepHome)
	}
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, createFailed(rp.name, err)
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

// resources is what a container asks of the cluster. Memory asked for is
// guaranteed and capped at it (the limit is the same, unless the container
// has a limit of its own); a CPU limit is set only if the container has one.
// What a container leaves out is the runner's own setting.
func (k *Kubernetes) resources(sp ContainerSpec) corev1.ResourceRequirements {
	res := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	cpu := sp.CPU
	if cpu == "" {
		cpu = k.o.CPU
	}
	cpuReq := resource.MustParse(cpu)
	if sp.CPULimit != "" {
		lim := resource.MustParse(sp.CPULimit)
		res.Limits[corev1.ResourceCPU] = lim
		if cpuReq.Cmp(lim) > 0 {
			cpuReq = lim.DeepCopy()
		}
	}
	res.Requests[corev1.ResourceCPU] = cpuReq
	switch {
	case sp.Memory != "" || sp.MemoryLimit != "": // asked for explicitly: guarantee it, and cap it
		req, lim := sp.Memory, sp.MemoryLimit
		if lim == "" {
			lim = req
		}
		if req == "" {
			req = lim
		}
		res.Requests[corev1.ResourceMemory] = resource.MustParse(req)
		res.Limits[corev1.ResourceMemory] = resource.MustParse(lim)
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
	restricted := k.o.restricted()
	mounts := []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "launchway", MountPath: "/launchway"}}
	volumes := []corev1.Volume{
		{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "launchway", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
	}
	if restricted {
		mounts = append(mounts, corev1.VolumeMount{Name: "home", MountPath: stepHome}, corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"})
		volumes = append(volumes,
			corev1.Volume{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			corev1.Volume{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	}
	containers := map[string]string{}
	images := map[string]string{}
	cs := []corev1.Container{{Name: helperName, Image: k.o.HelperImage, Command: keepAlive, WorkingDir: "/workspace",
		VolumeMounts: mounts, Resources: k.resources(ContainerSpec{})}}
	for _, sp := range specs {
		images[sp.Name] = sp.Image
		c := corev1.Container{Name: sp.Name, Image: sp.Image, WorkingDir: "/workspace", VolumeMounts: mounts,
			Resources: k.resources(sp), ImagePullPolicy: corev1.PullIfNotPresent}
		if sp.Service {
			c.Command = sp.Command // nil: the image's own entrypoint
			c.WorkingDir = ""
			c.ReadinessProbe = readinessProbe(sp.Ready)
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
	var podSecurity *corev1.PodSecurityContext
	if restricted {
		podSecurity = &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr(true),
			RunAsUser:      ptr(*k.o.RunAsUser),
			RunAsGroup:     ptr(*k.o.RunAsGroup),
			FSGroup:        ptr(*k.o.RunAsGroup),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}
		for i := range cs {
			cs[i].SecurityContext = &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			}
			// A step's own environment is exported when it starts, so it
			// wins over these; a service's settings are kept as they are.
			for _, e := range homeEnv {
				if !hasEnv(cs[i].Env, e.Name) {
					cs[i].Env = append(cs[i].Env, e)
				}
			}
		}
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
			SecurityContext:               podSecurity,
			Containers:                    cs,
			Volumes:                       volumes,
		},
	}
	for _, s := range k.o.ImagePullSecrets {
		pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: s})
	}
	return pod, containers
}

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
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
	readySince, noted := map[string]time.Time{}, map[string]bool{}
	for {
		pod, err := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return infra("waiting for pod %s: %v", name, err)
		}
		if pod.Status.Phase == corev1.PodRunning && allReady(pod) {
			k.noteReady(sys, readySince, noted)
			return nil
		}
		if err := k.waitForServices(ctx, pod, specs, sys, readySince, noted); err != nil {
			return err
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return cannotStart("the pod stopped: %s", podProblem(pod))
		}
		for _, st := range pod.Status.ContainerStatuses {
			if w := st.State.Waiting; w != nil {
				switch w.Reason {
				case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull":
					return cannotStart("cannot pull %s: %s", st.Image, oneLine(w.Message))
				case "CreateContainerConfigError", "CreateContainerError":
					return cannotStart("cannot start a container for %s: %s", st.Image, oneLine(w.Message))
				}
			}
			if t := st.State.Terminated; t != nil {
				if service[st.Name] {
					return cannotStart("the service %s stopped (exit %d): %s", st.Name, t.ExitCode, k.tail(ctx, name, st.Name))
				}
				return fmt.Errorf("the container %s (%s) stopped at once (exit %d). Its image needs /bin/sh", st.Name, st.Image, t.ExitCode)
			}
		}
		if note := podProblem(pod); note != lastNote {
			sys("Waiting for the pod: %s", note)
			lastNote = note
		}
		if time.Now().After(deadline) {
			return cannotStart("the pod did not start within %s: %s", k.startWait, podProblem(pod))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
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
	// Without a security context the containers may run as different users
	// (root, or each image's own), so everything is made writable by all.
	// In a restricted pod every container runs as the user that extracts the
	// files, so they are its own; only /workspace itself, the volume, is not
	// (it belongs to root and the pod's group), so Git is told it is safe.
	cmd := "tar xf - -C /workspace && chmod -R a+rwX /workspace"
	if k.o.restricted() {
		cmd = fmt.Sprintf(`tar xf - -C /workspace && { [ -e %[1]s/.gitconfig ] || printf '[safe]\n\tdirectory = /workspace\n' > %[1]s/.gitconfig; }`, stepHome)
	}
	var stderr bytes.Buffer
	err := k.exec(ctx, pod, helperName, []string{"sh", "-c", cmd}, pr, io.Discard, &stderr)
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

// CollectFiles reads report files from the run's pod, where the workspace
// is: the helper container lists the files below each glob's directory, and
// reads each chosen file, at most as much as is left of the limits. Only
// regular files are read; symbolic links are not followed.
func (k *Kubernetes) CollectFiles(ctx context.Context, s Spec, globs []string, lim CollectLimits) (*Collected, error) {
	out := &Collected{}
	pod := podName(s.RunID, k.owner)
	k.mu.Lock()
	if rp := k.pods[s.RunID]; rp != nil {
		pod = rp.name
	}
	k.mu.Unlock()
	p := newPlanner(globs, lim)
	searched := map[string]bool{}
	for _, g := range globs {
		base, depth := globBase(g)
		key := fmt.Sprintf("%s\x00%d", base, depth)
		if searched[key] {
			continue
		}
		searched[key] = true
		// find prints NUL-separated paths, so any name is read correctly. It
		// does not follow symbolic links. ** never enters .git or
		// node_modules unless a glob names them.
		args := []string{"find", base}
		if depth >= 0 {
			args = append(args, "-maxdepth", fmt.Sprint(depth))
		}
		var prune []string
		for _, d := range skippedDirs {
			if !explicitlyNamed(globs, path.Join(base, d)) && base != d && !strings.HasPrefix(base, d+"/") {
				prune = append(prune, d)
			}
		}
		if len(prune) > 0 {
			args = append(args, "(")
			for i, d := range prune {
				if i > 0 {
					args = append(args, "-o")
				}
				args = append(args, "-name", d)
			}
			args = append(args, ")", "-prune", "-o")
		}
		args = append(args, "-type", "f", "-print0")
		var list, stderr bytes.Buffer
		lw := &limitedWriter{w: &list, n: 16 << 20}
		script := `cd /workspace || exit 1; [ -d "$1" ] && [ ! -L "$1" ] || exit 0; exec find "$@"`
		err := k.exec(ctx, pod, helperName, append([]string{"sh", "-c", script, "sh"}, args[1:]...), nil, lw, &stderr)
		if err != nil && !lw.full {
			return out, infra("listing files in pod %s: %v %s", pod, err, strings.TrimSpace(stderr.String()))
		}
		if lw.full {
			p.note("the workspace has too many files where %s looks; only part of it was searched", g)
		}
		for _, name := range strings.Split(list.String(), "\x00") {
			name = strings.TrimPrefix(name, "./")
			if name == "" || strings.ContainsAny(name, "\n\r") {
				continue
			}
			p.offer(name)
		}
	}
	var used int64
	for _, name := range p.chosen() {
		n := p.budget(used)
		if n <= 0 {
			p.note("%s was not collected: the limit of %s for all files together was reached", name, bytesText(lim.MaxBytes))
			continue
		}
		var data, stderr bytes.Buffer
		lw := &limitedWriter{w: &data, n: n + 1}
		// Only a regular file, never what a symbolic link points at.
		err := k.exec(ctx, pod, helperName, []string{"sh", "-c",
			`cd /workspace && [ -f "$1" ] && [ ! -L "$1" ] && [ "$(readlink -f "./$1")" = "/workspace/$1" ] || { echo "not a regular file inside the workspace" >&2; exit 3; }; exec head -c "$2" -- "$1"`,
			"sh", name, fmt.Sprint(n + 1)}, nil, lw, &stderr)
		if err != nil && !lw.full {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			p.note("%s could not be read: %s", name, oneLine(msg))
			continue
		}
		f := CollectedFile{Path: name, Data: data.Bytes()}
		if int64(len(f.Data)) > n {
			f.Data, f.Cut = f.Data[:n], true
			p.cutNote(name, n, 0)
		}
		used += int64(len(f.Data))
		out.Files = append(out.Files, f)
	}
	out.Notes = p.notes
	return out, nil
}

// limitedWriter keeps at most n bytes and then says it is full.
type limitedWriter struct {
	w    io.Writer
	n    int64
	full bool
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > l.n {
		l.full = true
		b = b[:l.n]
	}
	n, err := l.w.Write(b)
	l.n -= int64(n)
	if err == nil && l.full {
		return n, io.ErrShortWrite
	}
	return n, err
}
