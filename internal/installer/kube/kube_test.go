package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"k8s.io/apimachinery/pkg/api/resource"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jk-magic-creations/launchway/internal/installer"
)

// fakeCluster is a cluster the installer talks to in tests: a fake clientset, and an applier that
// behaves like a healthy cluster (deployments become ready, jobs finish and make the setup code).
type fakeCluster struct {
	cs       *fake.Clientset
	applied  []string
	removed  []string
	name     string
	failPull bool
}

type clusterOpts struct {
	version        string
	canCreateNS    bool
	nsExists       bool
	canInNS        bool
	storageDefault bool
	storageNames   []string
	ingressClasses []string
	nodes          int
	cniPods        []string
	groups         []string
}

func newCluster(t *testing.T, o clusterOpts) *fakeCluster {
	t.Helper()
	cs := fake.NewSimpleClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: o.version}
	for _, g := range o.groups {
		cs.Resources = append(cs.Resources, &metav1.APIResourceList{GroupVersion: g})
	}
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		ra := r.Spec.ResourceAttributes
		allowed := o.canInNS
		if ra.Resource == "namespaces" {
			allowed = o.canCreateNS
		}
		r.Status.Allowed = allowed
		return true, r, nil
	})
	if o.nsExists {
		_, _ = cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "launchway"}}, metav1.CreateOptions{})
	}
	for i, n := range o.storageNames {
		sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: n}}
		if i == 0 && o.storageDefault {
			sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
		}
		_, _ = cs.StorageV1().StorageClasses().Create(context.Background(), sc, metav1.CreateOptions{})
	}
	for _, n := range o.ingressClasses {
		_, _ = cs.NetworkingV1().IngressClasses().Create(context.Background(), &netv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: n}}, metav1.CreateOptions{})
	}
	for i := 0; i < o.nodes; i++ {
		_, _ = cs.CoreV1().Nodes().Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n" + string(rune('a'+i))},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resourceQ("4"), corev1.ResourceMemory: resourceQ("8Gi")}}}, metav1.CreateOptions{})
	}
	for _, p := range o.cniPods {
		_, _ = cs.CoreV1().Pods("kube-system").Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: p, Namespace: "kube-system"}}, metav1.CreateOptions{})
	}
	return &fakeCluster{cs: cs, name: "kind-test"}
}

func (f *fakeCluster) Clientset() kubernetes.Interface { return f.cs }
func (f *fakeCluster) Name() string                    { return f.name }
func (f *fakeCluster) Address() string                 { return "https://127.0.0.1:6443" }
func (f *fakeCluster) RESTConfig() *rest.Config        { return nil }
func (f *fakeCluster) Remove(_ context.Context, o *unstructured.Unstructured) error {
	f.removed = append(f.removed, o.GetKind()+"/"+o.GetName())
	return nil
}
func (f *fakeCluster) Apply(ctx context.Context, o *unstructured.Unstructured) error {
	f.applied = append(f.applied, o.GetKind()+"/"+o.GetName())
	ns := o.GetNamespace()
	switch o.GetKind() {
	case "Namespace":
		_, _ = f.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: o.GetName()}}, metav1.CreateOptions{})
	case "Deployment":
		n := int32(1)
		if r, ok, _ := unstructured.NestedInt64(o.Object, "spec", "replicas"); ok {
			n = int32(r)
		}
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: o.GetName(), Namespace: ns}, Spec: appsv1.DeploymentSpec{Replicas: &n}, Status: appsv1.DeploymentStatus{ReadyReplicas: n}}
		if f.failPull {
			d.Status.ReadyReplicas = 0
			_, _ = f.cs.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: o.GetName() + "-x", Namespace: ns},
				Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{Name: "server", Image: "ghcr.io/x/launchway:v1",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull access denied"}}}}}}, metav1.CreateOptions{})
		}
		_, _ = f.cs.AppsV1().Deployments(ns).Create(ctx, d, metav1.CreateOptions{})
	case "Job":
		_, _ = f.cs.BatchV1().Jobs(ns).Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: o.GetName(), Namespace: ns}, Status: batchv1.JobStatus{Succeeded: 1}}, metav1.CreateOptions{})
		_, _ = f.cs.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "launchway-setup-code", Namespace: ns}, Data: map[string][]byte{"code": []byte("AAAAA-BBBBB-CCCCC-DDDDD-EEEEE")}}, metav1.CreateOptions{})
	}
	return nil
}

func resourceQ(s string) resource.Quantity { return resource.MustParse(s) }

type harness struct {
	t   *testing.T
	out *bytes.Buffer
	env installer.Env
	dir string
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, out: &bytes.Buffer{}, dir: t.TempDir()}
	h.env = installer.Env{In: strings.NewReader(""), Out: h.out, GOOS: "linux", Getenv: func(string) string { return "" }, Now: time.Now,
		Version: "dev", Reachable: func(string) bool { return false }}
	return h
}

func (h *harness) run(c *fakeCluster, answers map[string]string, o installer.Options, in *Installer) (installer.Result, error) {
	if in == nil {
		in = &Installer{}
	}
	in.Connect = func(string, string) (Cluster, error) { return c, nil }
	in.Poll = time.Millisecond
	in.Sleep = func(context.Context, time.Duration) error { return nil }
	in.Forward = func(context.Context, Cluster, string, int, int, io.Writer) (func(), error) { return func() {}, nil }
	in.SetupDone = func(context.Context, string) bool { return true }
	o.Answers = answers
	o.NoBrowser = true
	o.Assume = true
	o.TranscriptTo = &bytes.Buffer{}
	o.Kube = in
	answers["target"] = "kubernetes"
	if _, ok := answers["kubernetes.values_file"]; !ok {
		answers["kubernetes.values_file"] = filepath.Join(h.dir, "values.yaml")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return installer.Run(ctx, h.env, o)
}

func healthy() clusterOpts {
	return clusterOpts{version: "v1.31.0", canCreateNS: true, canInNS: true, storageDefault: true, storageNames: []string{"standard"}, nodes: 2, cniPods: []string{"cilium-abcde"}}
}

func baseAnswers() map[string]string {
	return map[string]string{"kubernetes.confirm": "kind-test", "kubernetes.image": "ghcr.io/x/launchway:v1", "kubernetes.way_in": "forward", "kubernetes.keep_forward": "no"}
}

func TestHealthyClusterInstallsFromTheEmbeddedChart(t *testing.T) {
	h := newHarness(t)
	c := newCluster(t, healthy())
	res, err := h.run(c, baseAnswers(), installer.Options{}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	vals, _ := os.ReadFile(filepath.Join(h.dir, "values.yaml"))
	if n := strings.Count(string(vals), "\n"); n > 20 {
		t.Fatalf("the values file is %d lines; it must stay small:\n%s", n, vals)
	}
	if strings.Contains(string(vals), "AAAAA") {
		t.Fatal("a secret in the values file")
	}
	joined := strings.Join(c.applied, " ")
	for _, want := range []string{"Namespace/launchway", "Job/", "Deployment/", "NetworkPolicy/"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("not applied: %s\napplied: %s", want, joined)
		}
	}
	if !strings.Contains(h.out.String(), "AAAAA-BBBBB") {
		t.Fatalf("the setup code is not shown:\n%s", h.out)
	}
	if !strings.HasPrefix(res.Address, "http://127.0.0.1:") {
		t.Fatalf("address %q", res.Address)
	}
	if !strings.Contains(h.out.String(), "helm upgrade --install") {
		t.Fatal("the equivalent helm command is not shown")
	}
}

func TestWrongConfirmationChangesNothing(t *testing.T) {
	h := newHarness(t)
	c := newCluster(t, healthy())
	a := baseAnswers()
	a["kubernetes.confirm"] = "some-other-cluster"
	_, err := h.run(c, a, installer.Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("got %v", err)
	}
	if len(c.applied) != 0 {
		t.Fatal("something was applied")
	}
}

func TestNoClusterSaysSoAndWhatToDo(t *testing.T) {
	h := newHarness(t)
	in := &Installer{}
	in.Connect = func(string, string) (Cluster, error) { return nil, errors.New("no cluster is selected") }
	_, err := installerRun(h, in, baseAnswers())
	if err == nil || !strings.Contains(err.Error(), "No cluster was changed") {
		t.Fatalf("got %v", err)
	}
}

func installerRun(h *harness, in *Installer, answers map[string]string) (installer.Result, error) {
	answers["target"] = "kubernetes"
	return installer.Run(context.Background(), h.env, installer.Options{Answers: answers, Assume: true, NoBrowser: true, TranscriptTo: &bytes.Buffer{}, Kube: in})
}

func TestTooOldKubernetes(t *testing.T) {
	h := newHarness(t)
	o := healthy()
	o.version = "v1.27.3"
	c := newCluster(t, o)
	_, err := h.run(c, baseAnswers(), installer.Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs 1.30.0 or newer") {
		t.Fatalf("got %v", err)
	}
}

func TestNoRightsToCreateANamespaceAsksForAnExistingOne(t *testing.T) {
	h := newHarness(t)
	o := healthy()
	o.canCreateNS = false
	o.nsExists = false
	c := newCluster(t, o)
	a := baseAnswers()
	a["kubernetes.existing_namespace"] = "team-a"
	_, err := h.run(c, a, installer.Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "team-a") {
		t.Fatalf("a namespace that does not exist must be refused by name: %v", err)
	}
	if !strings.Contains(h.out.String(), "do not have the right to create a namespace") {
		t.Fatal("the missing right is not explained")
	}
	if len(c.applied) != 0 {
		t.Fatal("applied without rights")
	}
}

func TestNoStorageClassOffersTrialAndLabelsIt(t *testing.T) {
	h := newHarness(t)
	o := healthy()
	o.storageDefault, o.storageNames = false, nil
	c := newCluster(t, o)
	a := baseAnswers()
	a["kubernetes.storage"] = "trial"
	if _, err := h.run(c, a, installer.Options{}, nil); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if !strings.Contains(h.out.String(), "TRIAL STORAGE") {
		t.Fatal("trial storage is not labelled")
	}
	for _, x := range c.applied {
		if strings.HasPrefix(x, "PersistentVolumeClaim/") {
			t.Fatal("a volume claim was made in trial mode")
		}
	}
}

func TestNoStorageAtAllCannotChooseAClass(t *testing.T) {
	h := newHarness(t)
	o := healthy()
	o.storageDefault, o.storageNames = false, nil
	c := newCluster(t, o)
	a := baseAnswers()
	a["kubernetes.storage"] = "class"
	_, err := h.run(c, a, installer.Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no storage classes") {
		t.Fatalf("got %v", err)
	}
}

func TestIngressWithCertManagerBecomesValues(t *testing.T) {
	h := newHarness(t)
	o := healthy()
	o.ingressClasses = []string{"nginx"}
	c := newCluster(t, o)
	a := baseAnswers()
	a["kubernetes.way_in"] = "ingress:nginx"
	a["kubernetes.host"] = "launchway.example.com"
	a["kubernetes.tls"] = "secret"
	a["kubernetes.tls_secret"] = "lw-cert"
	// the way in is not reachable from the test machine, so the port-forward is offered
	if _, err := h.run(c, a, installer.Options{}, nil); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	vals, _ := os.ReadFile(filepath.Join(h.dir, "values.yaml"))
	for _, want := range []string{"externalURL: https://launchway.example.com", "className: nginx", "secretName: lw-cert"} {
		if !strings.Contains(string(vals), want) {
			t.Fatalf("values lack %q:\n%s", want, vals)
		}
	}
}

func TestNetworkPolicyNotEnforcedIsSaid(t *testing.T) {
	h := newHarness(t)
	o := healthy()
	o.cniPods = []string{"kube-flannel-ds-x"}
	c := newCluster(t, o)
	if _, err := h.run(c, baseAnswers(), installer.Options{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "will be installed but will NOT take effect") {
		t.Fatalf("the lockdown not taking effect must be said:\n%s", h.out)
	}
}

func TestGitOpsWritesFilesAndAppliesNothing(t *testing.T) {
	h := newHarness(t)
	o := healthy()
	o.groups = []string{"argoproj.io/v1alpha1"}
	c := newCluster(t, o)
	c.cs.Resources = append(c.cs.Resources[:0], &metav1.APIResourceList{GroupVersion: "argoproj.io/v1alpha1", APIResources: []metav1.APIResource{{Name: "applications", Kind: "Application"}}})
	a := baseAnswers()
	a["kubernetes.gitops"] = "files"
	if _, err := h.run(c, a, installer.Options{}, nil); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if len(c.applied) != 0 {
		t.Fatalf("GitOps files mode applied %v", c.applied)
	}
	app, err := os.ReadFile(filepath.Join(h.dir, "launchway-application.yaml"))
	if err != nil || !strings.Contains(string(app), "kind: Application") {
		t.Fatalf("%v %s", err, app)
	}
}

func TestDevBuildNeedsAnImageAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	c := newCluster(t, healthy())
	a := baseAnswers()
	delete(a, "kubernetes.image")
	_, err := h.run(c, a, installer.Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "kubernetes.image") {
		t.Fatalf("got %v", err)
	}
}

func TestImagePullFailureIsExplainedInPlainWords(t *testing.T) {
	h := newHarness(t)
	c := newCluster(t, healthy())
	c.failPull = true
	_, err := h.run(c, baseAnswers(), installer.Options{}, &Installer{Limit: time.Second})
	if err == nil || !strings.Contains(err.Error(), "cannot download the image") || !strings.Contains(err.Error(), "pull secret") {
		t.Fatalf("got %v", err)
	}
}

func TestDryRunWritesNothingAndAppliesNothing(t *testing.T) {
	h := newHarness(t)
	c := newCluster(t, healthy())
	if _, err := h.run(c, baseAnswers(), installer.Options{DryRun: true}, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.applied) != 0 {
		t.Fatal("dry run applied")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "values.yaml")); err == nil {
		t.Fatal("dry run wrote the values file")
	}
	if !strings.Contains(h.out.String(), "Dry run: nothing was changed") {
		t.Fatal("dry run is not said")
	}
}

func TestUninstallSaysWhatItKeeps(t *testing.T) {
	h := newHarness(t)
	c := newCluster(t, healthy())
	a := baseAnswers()
	if _, err := h.run(c, a, installer.Options{Uninstall: true}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "ROOT KEY") {
		t.Fatalf("uninstall must name the root key:\n%s", h.out)
	}
}
