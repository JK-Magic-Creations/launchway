package executor

// One pod for each step attempt (kubernetes.pod: per-step, the default).
//
// A step's pod holds the step's own container (its image, its own requests
// and limits), the helper container that holds the workspace, and the run's
// service containers, and nothing else. It is made when the step starts and
// deleted when the step ends, however it ends (success, failure, cancel,
// timeout, lost lease), so the namespace's quota is held only while a step
// runs, and two steps that use one image have a limit each.
//
// The workspace between steps. A pod's workspace is an emptyDir, which goes
// with the pod, so the Station keeps the run's workspace on its own disk, in a
// copy that the checkout is first copied into (the checkout itself is never
// written to: for a run from a directory it is the person's own files):
//
//   - a step's pod is filled from the copy when it starts (uploadDir);
//   - when the step ends, before its pod is deleted, the pod's whole
//     workspace is read back into the copy (handBack): files the step wrote or
//     changed are kept, files it removed are removed, files another step of
//     the run that ran at the same time left meanwhile are not touched;
//   - artifacts, caches, test reports and build contexts are read from the
//     copy, and artifacts and caches that a step fetches or restores are put in
//     it, before the step's pod exists (see CollectFiles, StageFiles, PlaceFiles).
//
// The copy goes through the Station, as the first step's workspace always
// did, and costs a transfer of the workspace in each direction for each step
// (see BenchmarkStepWorkspaceHandOver). The alternative, a volume claimed
// once for the run and mounted by each step's pod in turn, avoids the
// transfer but needs a storage class, a claim and ReadWriteOnce, which
// confines the steps of a stage that run at the same time to one node;
// docs/development/automatic-resources.md says why it was not chosen first.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Values of KubernetesOptions.Pod.
const (
	PodPerStep = "per-step" // a pod for each step attempt (the default)
	PodPerRun  = "per-run"  // one pod for the whole run (the earlier behavior; to be removed)
)

// perStep says that each step attempt has a pod of its own. A Kubernetes
// made without the defaults (setDefaults) keeps the earlier behavior.
func (k *Kubernetes) perStep() bool { return k.o.Pod == PodPerStep }

// PerStep says whether the executor gives each step a pod of its own, so
// that the container a step is given is sized for that step alone.
func (k *Kubernetes) PerStep() bool { return k.perStep() }

// podNoun is what the pod is called in a message.
func (k *Kubernetes) podNoun() string {
	if k.perStep() {
		return "the step's pod"
	}
	return "the run's pod"
}

// handOverLimits bound what a step's pod hands back: the whole workspace, on
// the Station's disk.
var handOverLimits = ExportLimits{MaxBytes: 32 << 30, MaxFiles: 5_000_000}

// handBackWithin is how long the workspace may take to come back from a pod
// whose step ended by itself; a step that was canceled gets a short time, as
// the run is going away.
const (
	handBackWithin   = 10 * time.Minute
	handBackCanceled = 30 * time.Second
)

// stepPodName names the pod of one step attempt. It is a valid DNS label
// (at most 63 characters, lower case letters, digits and '-'), is made from the
// run, the step and the attempt, and is different for each lease, so no two
// attempts, and no two Stations, ever share a name. The step and attempt
// are readable in it; the run is in the pod's labels.
func stepPodName(s Spec, owner string) string {
	step := s.StepID
	if step == "" {
		step = s.Name
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{s.RunID, step, strconv.Itoa(s.Attempt), s.LeaseID, s.Name, owner}, "\x00")))
	return fmt.Sprintf("launchway-%s-a%d-%s", dnsPart(step, 20), s.Attempt, hex.EncodeToString(sum[:5]))
}

// dnsPart turns text into at most n characters of a DNS label.
func dnsPart(text string, n int) string {
	var b strings.Builder
	dash := true // no dash at the start
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := b.String()
	if len(out) > n {
		out = out[:n]
	}
	if out = strings.TrimRight(out, "-"); out == "" {
		return "step"
	}
	return out
}

// stepContainers is what a step's pod holds besides the helper: the step's
// own container and the run's services.
func stepContainers(s Spec) []ContainerSpec {
	var out []ContainerSpec
	for _, c := range s.Containers {
		if c.Service || c.Name == s.Container {
			out = append(out, c)
		}
	}
	return out
}

// runInStepPod runs a step in a pod of its own.
func (k *Kubernetes) runInStepPod(ctx context.Context, s Spec, out Sink) (int, error) {
	sys := func(format string, a ...any) { out("system", fmt.Sprintf(format, a...)) }
	name := stepPodName(s, k.owner)
	defer k.holdRun(s.RunID)()
	k.setActive(name, s.RunID)
	defer k.clearActive(name)

	m, err := k.mirrorFor(s)
	if err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, infra("preparing the run's workspace on the Station: %v", err)
	}
	// What an earlier attempt of this run left in the namespace, which this
	// Station made and no step of this process runs in, is released first, so
	// that a retry never waits for room its own run holds.
	k.releaseOwnIdle(ctx, s.RunID, sys)

	s.Containers = stepContainers(s)
	pod, containers, err := k.preparePod(ctx, &s, name, sys)
	if err != nil {
		return -1, err
	}
	pod.Labels[labelKind] = "step"
	pod.Annotations[annoStep] = s.StepID
	pod.Annotations[annoAttempt] = strconv.Itoa(s.Attempt)
	pod.Annotations[annoReady] = s.WorkspaceID
	var names []string
	for _, c := range s.Containers {
		kind := ""
		if c.Service {
			kind = " (service)"
		}
		names = append(names, c.Name+kind)
	}
	sys("Starting pod %s/%s for this step: %s", k.o.Namespace, name, strings.Join(names, ", "))
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
	pods := k.client.CoreV1().Pods(k.o.Namespace)
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, k.createFailed(ctx, name, s.RunID, err)
	}
	defer k.deleteStepPod(ctx, name, sys)
	if err := k.waitRunning(ctx, name, s.Containers, sys); err != nil {
		return -1, err
	}
	took := time.Now()
	base, err := k.uploadMirror(ctx, name, m)
	if err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, infra("copying the workspace into the pod: %v", err)
	}
	sys("Copied the workspace into the pod in %s", time.Since(took).Round(10*time.Millisecond))

	rp := &runPod{name: name, workspace: s.WorkspaceID, containers: containers, ready: true}
	code, runErr := k.execStep(ctx, s, rp, out)
	if code == 137 {
		if note := k.explainFromPod(context.WithoutCancel(ctx), name, containers[s.Container], code); note != "" {
			k.setNote(s.Name, note)
		}
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) && ctx.Err() == nil {
		return code, runErr // the pod is gone or unreachable: nothing to read back
	}
	within := handBackWithin
	if ctx.Err() != nil {
		within = handBackCanceled
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), within)
	defer cancel()
	took = time.Now()
	if herr := k.handBack(hctx, name, m, base); herr != nil {
		if code == 0 && runErr == nil {
			return -1, infra("copying the workspace back from the pod: %v", herr)
		}
		sys("The files this step left could not be copied back from its pod (%v); reports and artifacts of this step may be missing", herr)
	} else if ctx.Err() == nil {
		sys("Copied the workspace back in %s", time.Since(took).Round(10*time.Millisecond))
	}
	return code, runErr
}

// deleteStepPod removes the pod of a step that ended, at once.
func (k *Kubernetes) deleteStepPod(ctx context.Context, name string, sys func(string, ...any)) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))})
	if err != nil && !apierrors.IsNotFound(err) {
		sys("Could not remove the step's pod %s (%v); it is removed later by the Station's cleanup", name, err)
	}
}

func (k *Kubernetes) setActive(pod, run string) {
	k.mu.Lock()
	if k.active == nil {
		k.active = map[string]string{}
	}
	k.active[pod] = run
	k.mu.Unlock()
}

func (k *Kubernetes) clearActive(pod string) {
	k.mu.Lock()
	delete(k.active, pod)
	k.mu.Unlock()
}

func (k *Kubernetes) isActive(pod string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.active[pod]
	return ok
}

func (k *Kubernetes) setNote(key, note string) {
	k.mu.Lock()
	if k.notes == nil {
		k.notes = map[string]string{}
	}
	k.notes[key] = note
	k.mu.Unlock()
}

func (k *Kubernetes) takeNote(key string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := k.notes[key]
	delete(k.notes, key)
	return n
}

// releaseOwnIdle deletes this Station's pods of the run that no step of this
// process runs in: the pods of earlier attempts whose lease the server took
// back, of a process that stopped, or of steps that ended without their pod
// going. They hold quota for nothing, and a retry that waits for room its own
// run holds waits for ever. It says what it released. It returns how many.
func (k *Kubernetes) releaseOwnIdle(ctx context.Context, runID string, sys func(string, ...any)) int {
	if sys == nil {
		sys = func(string, ...any) {}
	}
	sel := fmt.Sprintf("%s=launchway,%s=%s,%s=%s", labelManaged, labelRun, runID, labelOwner, k.owner)
	list, err := k.client.CoreV1().Pods(k.o.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range list.Items {
		if p.DeletionTimestamp != nil || k.isActive(p.Name) {
			continue
		}
		held := holdOf([]corev1.Pod{p})
		if err := k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))}); err != nil && !apierrors.IsNotFound(err) {
			continue
		}
		n++
		what := p.Annotations[annoStep]
		if what != "" {
			what = fmt.Sprintf("step %s, attempt %s", what, p.Annotations[annoAttempt])
		} else {
			what = "the run's earlier pod"
		}
		sys("Released pod %s of this run (%s): its attempt no longer holds a lease, so it only held %s", p.Name, what, describeAmount("requests.memory", held.total["requests.memory"]))
	}
	return n
}

// explainFromPod looks at a container of a pod that is still there.
func (k *Kubernetes) explainFromPod(ctx context.Context, pod, container string, code int) string {
	p, err := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	var st *corev1.ContainerStatus
	for i := range p.Status.ContainerStatuses {
		if p.Status.ContainerStatuses[i].Name == container {
			st = &p.Status.ContainerStatuses[i]
		}
	}
	var spec *corev1.Container
	for i := range p.Spec.Containers {
		if p.Spec.Containers[i].Name == container {
			spec = &p.Spec.Containers[i]
		}
	}
	return memoryKillNote(st, spec, code)
}

// A mirror is the Station's copy of a run's workspace, from which each step's
// pod is filled and into which it is read back.
type mirror struct {
	mu   sync.RWMutex // read while a pod is filled or files are read; written while files are put in or merged
	dir  string
	wsid string
	// ready is set when the copy of the checkout is complete.
	ready bool
}

// workRoot is where the Station keeps its copies of workspaces.
func (k *Kubernetes) workRoot() string {
	if k.o.WorkDir != "" {
		return k.o.WorkDir
	}
	return filepath.Join(os.TempDir(), "launchway-kubernetes-"+k.owner)
}

// safeName makes text fit as the name of a file.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

func (k *Kubernetes) mirrorPaths(runID string) (dir, marker string) {
	root := k.workRoot()
	n := safeName(runID)
	return filepath.Join(root, n), filepath.Join(root, n+".id")
}

// mirrorFor returns the run's workspace copy, making it from the Station's
// checkout the first time. A copy left by an earlier process of this Station
// for the same workspace is used as it is; one for another workspace of the
// run (the run was moved and came back) is replaced.
func (k *Kubernetes) mirrorFor(s Spec) (*mirror, error) {
	dir, marker := k.mirrorPaths(s.RunID)
	k.mu.Lock()
	if k.mirrors == nil {
		k.mirrors = map[string]*mirror{}
	}
	m := k.mirrors[s.RunID]
	if m == nil {
		m = &mirror{dir: dir}
		k.mirrors[s.RunID] = m
	}
	k.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ready && m.wsid == s.WorkspaceID {
		return m, nil
	}
	if id, err := os.ReadFile(marker); err == nil && string(id) == s.WorkspaceID {
		if _, err := os.Stat(dir); err == nil {
			m.wsid, m.ready = s.WorkspaceID, true
			return m, nil
		}
	}
	_ = os.Remove(marker)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := copyTree(s.Workspace, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("copying the checkout: %w", err)
	}
	if err := os.WriteFile(marker, []byte(s.WorkspaceID), 0o600); err != nil {
		return nil, err
	}
	m.wsid, m.ready = s.WorkspaceID, true
	return m, nil
}

// workspaceMirror returns the run's copy if it exists, without making it.
func (k *Kubernetes) workspaceMirror(s Spec) *mirror {
	dir, marker := k.mirrorPaths(s.RunID)
	k.mu.Lock()
	if k.mirrors == nil {
		k.mirrors = map[string]*mirror{}
	}
	m := k.mirrors[s.RunID]
	if m == nil {
		m = &mirror{dir: dir}
		k.mirrors[s.RunID] = m
	}
	k.mu.Unlock()
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ready && m.wsid == s.WorkspaceID {
		return m
	}
	if id, err := os.ReadFile(marker); err == nil && string(id) == s.WorkspaceID {
		if _, err := os.Stat(dir); err == nil {
			return m
		}
	}
	return nil
}

// WorkspaceDir implements WorkspaceDirer: the Station's copy of the run's
// workspace, if the run has one.
func (k *Kubernetes) WorkspaceDir(s Spec) string {
	if !k.perStep() {
		return ""
	}
	if m := k.workspaceMirror(s); m != nil {
		return m.dir
	}
	return ""
}

// WorkspaceDirer is implemented by executors that keep the run's workspace in
// a directory on the Station that is not the checkout.
type WorkspaceDirer interface {
	// WorkspaceDir is that directory, or "" when the checkout is the workspace.
	WorkspaceDir(s Spec) string
}

// PerStepSizer is implemented by executors that make a pod for each step, so
// that a step's container is the one sized for it alone.
type PerStepSizer interface {
	PerStep() bool
}

// copyTree copies the files of src into the empty directory dst, as they
// would be sent into a pod: directories, regular files and symbolic links.
func copyTree(src, dst string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTar(pw, src)) }()
	err := extractWorkspace(pr, dst, handOverLimits, nil, nil)
	pr.CloseWithError(errors.New("stopped reading"))
	return err
}

// uploadMirror fills the pod's workspace from the copy and returns the paths
// it sent, so that what the step removes can be removed from the copy when
// the files come back.
func (k *Kubernetes) uploadMirror(ctx context.Context, pod string, m *mirror) (map[string]fileFP, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	base := map[string]fileFP{}
	if err := k.uploadDir(ctx, pod, m.dir, base); err != nil {
		return nil, err
	}
	return base, nil
}

// handBack reads the pod's whole workspace into the copy: what the step wrote
// or changed replaces what the copy has, and what it removed (it was in the
// copy when the pod was filled and is not in the pod now) is removed. Paths
// that were not in the copy then, that another step of the run put there
// meanwhile, are not touched.
func (k *Kubernetes) handBack(ctx context.Context, pod string, m *mirror, base map[string]fileFP) error {
	pr, pw := io.Pipe()
	var stderr strings.Builder
	done := make(chan error, 1)
	go func() {
		err := k.exec(ctx, pod, helperName, []string{"sh", "-c", "cd /workspace && exec tar -c -f - ."}, nil, pw, &lockedWriter{w: &stderr})
		pw.CloseWithError(err)
		done <- err
	}()
	m.mu.Lock()
	seen := map[string]bool{}
	rerr := extractWorkspace(pr, m.dir, handOverLimits, base, seen)
	if rerr == nil {
		_, _ = io.Copy(io.Discard, pr) // the end of the archive is padding; let tar finish writing it
	}
	pr.CloseWithError(errors.New("stopped reading"))
	err := <-done
	if rerr == nil && err == nil && len(seen) > 0 {
		// Only a complete archive says what was removed.
		var gone []string
		for p := range base {
			if !seen[p] {
				gone = append(gone, p)
			}
		}
		slices.Sort(gone)
		if root, oerr := os.OpenRoot(m.dir); oerr == nil {
			for _, p := range gone {
				_ = root.RemoveAll(filepath.FromSlash(p))
			}
			root.Close()
		}
	}
	m.mu.Unlock()
	switch {
	case rerr != nil:
		return rerr
	case err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(oneLine(msg))
	}
	_ = os.Chtimes(m.dir, time.Now(), time.Now()) // so that the copy's age says when it was last used
	return nil
}

// lockedWriter lets two goroutines write to one builder.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// cleanMirrors removes the copies of workspaces of runs that ended, or that
// the server does not know, or that nothing has used for max_pod_age, unless
// a step of the run executes in this process.
func (k *Kubernetes) cleanMirrors(ctx context.Context, lookup RunLookup) error {
	entries, err := os.ReadDir(k.workRoot())
	if err != nil {
		return nil // none yet
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return nil
	}
	infos, err := lookup(ctx, ids)
	if err != nil {
		return err
	}
	k.mu.Lock()
	busy := make(map[string]bool, len(k.busy))
	for id := range k.busy {
		busy[safeName(id)] = true
	}
	k.mu.Unlock()
	for _, id := range ids {
		if busy[id] {
			continue
		}
		info, known := infos[id]
		dir := filepath.Join(k.workRoot(), id)
		var age time.Duration
		if fi, err := os.Stat(dir); err == nil {
			age = time.Since(fi.ModTime())
		}
		if !removable(info, known, true, 0) && (k.maxPodAge <= 0 || age <= k.maxPodAge) {
			continue
		}
		_ = os.Remove(filepath.Join(k.workRoot(), id+".id"))
		_ = os.RemoveAll(dir)
		k.mu.Lock()
		for run := range k.mirrors {
			if safeName(run) == id {
				delete(k.mirrors, run)
			}
		}
		k.mu.Unlock()
	}
	return nil
}

// stepPodRemovable decides whether the reaper deletes a step's pod. A pod of
// a step that executes in this process stays. One this Station made and no
// step of this process runs in is the pod of an attempt that no longer holds
// a lease (the process that made it stopped, or the lease was lost and the
// pod was not removed): it goes. Another Station's pod goes when its run
// ended, or when it is older than max_pod_age.
func (k *Kubernetes) stepPodRemovable(p *corev1.Pod, info RunInfo, known bool) bool {
	if p.DeletionTimestamp != nil || k.isActive(p.Name) {
		return false
	}
	if p.Labels[labelOwner] == k.owner {
		return true
	}
	if k.maxPodAge > 0 && time.Since(p.CreationTimestamp.Time) > k.maxPodAge {
		return true
	}
	return known && removable(info, true, false, 0)
}

// readWorkspace returns the directory to read a run's files from, and what
// to call when done: the run's copy, held against a step handing its files
// back meanwhile, or the checkout when the run has no copy yet.
func (k *Kubernetes) readWorkspace(s Spec) (string, func()) {
	if m := k.workspaceMirror(s); m != nil {
		m.mu.RLock()
		return m.dir, m.mu.RUnlock
	}
	return s.Workspace, func() {}
}
