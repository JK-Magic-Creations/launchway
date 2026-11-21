package kube

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/jk-magic-creations/launchway/internal/installer"
)

// DefaultImageRepo is where release images are published.
const DefaultImageRepo = "ghcr.io/jk-magic-creations/launchway"

// Installer is the Kubernetes target of `launchway install`.
type Installer struct {
	Kubeconfig, Context, Namespace string
	// Image overrides the image this build would install (a tag, or a digest reference).
	Image string
	// Connect reads the cluster; nil uses the real kubeconfig.
	Connect func(kubeconfig, context string) (Cluster, error)
	// Waiter timings, for tests.
	Poll  time.Duration
	Sleep func(context.Context, time.Duration) error
	// Limit is how long to wait for the installation to come up (default 8 minutes).
	Limit time.Duration
	// Forward opens the port-forward; nil uses the real one.
	Forward func(ctx context.Context, c Cluster, ns string, local, remote int, out io.Writer) (func(), error)
	// SetupDone reports whether the first administrator has been made (nil: poll GET /api/v1/setup).
	SetupDone func(ctx context.Context, url string) bool

	lastRelease string
}

type installed struct {
	s       Settings
	facts   *Facts
	trial   bool
	gitops  string
	write   bool
	values  string
	portFwd bool
	extraDB bool
}

func (in *Installer) connect() (Cluster, error) {
	if in.Connect != nil {
		return in.Connect(in.Kubeconfig, in.Context)
	}
	l, err := Connect(in.Kubeconfig, in.Context)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// Run implements installer.KubeInstaller.
func (in *Installer) Run(ctx context.Context, e *installer.Env, a *installer.Asker, o installer.Options, t *installer.Transcript) (installer.Result, error) {
	res := installer.Result{}
	cl, err := in.connect()
	if err != nil {
		return res, fmt.Errorf("%w\n\nNo cluster was changed. Make sure `kubectl get nodes` would work from this machine (a kubeconfig, and the right context), then run this again.", err)
	}
	e.Printf("\nThis is the cluster I would change:\n  context  %s\n  address  %s\n", cl.Name(), cl.Address())
	typed, err := a.Ask(installer.Question{Key: "kubernetes.confirm", Prompt: "Type the name of that cluster context to confirm it is the right one", Required: true,
		Help: "I will not touch a cluster you have not named. (To use a different one, stop here and run again with --context NAME or another KUBECONFIG.)"})
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(typed) != cl.Name() {
		return res, fmt.Errorf("you typed %q, but the cluster context is %q: nothing was changed", typed, cl.Name())
	}
	cs := cl.Clientset()
	ns := in.Namespace
	if ns == "" {
		ns, err = a.Ask(installer.Question{Key: "kubernetes.namespace", Prompt: "Which namespace should Launchway live in?", Default: "launchway"})
		if err != nil {
			return res, err
		}
	}
	release, err := a.Ask(installer.Question{Key: "kubernetes.release", Prompt: "Release name", Default: "launchway"})
	if err != nil {
		return res, err
	}
	if o.Uninstall {
		return in.uninstall(ctx, e, a, o, cl, ns, release, res)
	}
	facts, err := Detect(ctx, cs, ns)
	if err != nil {
		return res, err
	}
	in.report(e, facts, ns)
	st := &installed{facts: facts}
	st.s = Settings{Release: release, Namespace: ns}
	if err := in.askAll(ctx, e, a, st, ns, cl); err != nil {
		return res, err
	}
	return in.execute(ctx, e, a, o, t, cl, st, res)
}

// report prints what was found, in plain words, before any question is asked.
func (in *Installer) report(e *installer.Env, f *Facts, ns string) {
	ok := func(b bool) string {
		if b {
			return "ok"
		}
		return "NOT OK"
	}
	e.Printf("\nWhat I found in this cluster:\n")
	e.Printf("  Kubernetes %s: %s (Launchway needs %s or newer)\n", f.ServerVersion, ok(f.VersionOK), MinKubeVersion)
	switch {
	case f.CanCreateNamespace:
		e.Printf("  Rights: you may create namespaces (cluster-wide rights)\n")
	case f.NamespaceExists[ns] && f.CanInstallInNS[ns]:
		e.Printf("  Rights: you may install into the existing namespace %s only\n", ns)
	default:
		e.Printf("  Rights: you may NOT create namespaces here, and cannot install into %s\n", ns)
	}
	if f.DefaultStorage != "" {
		e.Printf("  Storage: default storage class %s\n", f.DefaultStorage)
	} else if len(f.StorageClasses) > 0 {
		e.Printf("  Storage: no default storage class (available: %s)\n", strings.Join(f.StorageClasses, ", "))
	} else {
		e.Printf("  Storage: no storage classes at all\n")
	}
	switch {
	case len(f.IngressClasses) > 0:
		e.Printf("  A way in: Ingress class %s\n", strings.Join(f.IngressClasses, ", "))
	case len(f.GatewayClasses) > 0:
		e.Printf("  A way in: Gateway API class %s\n", strings.Join(f.GatewayClasses, ", "))
	default:
		e.Printf("  A way in: no Ingress or Gateway API found (a port-forward will do for now)\n")
	}
	if f.CertManager {
		e.Printf("  TLS: cert-manager is installed (issuers: %s)\n", orNone(f.Issuers))
	} else {
		e.Printf("  TLS: no cert-manager\n")
	}
	switch f.NetpolEnforced {
	case Yes:
		e.Printf("  Network policies: enforced by %s\n", f.CNI)
	case No:
		e.Printf("  Network policies: NOT enforced (%s). The default-deny lockdown Launchway ships will be installed but will NOT take effect.\n", f.CNI)
	default:
		e.Printf("  Network policies: cannot tell whether this cluster enforces them. If it does not, the shipped lockdown will not take effect.\n")
	}
	if l := f.NamespacePSA[ns]; l != "" {
		e.Printf("  Pod security: namespace %s enforces %q (Launchway's pods meet the restricted level)\n", ns, l)
	}
	e.Printf("  Room: %d node(s), about %d m CPU and %s memory not yet requested (a small install asks for about %d m and %s)\n",
		facts(f).Nodes, f.FreeCPUMilli, quantity(f.FreeMemBytes), NeedCPUMilli, quantity(NeedMemBytes))
	if f.ArgoCD || f.Flux {
		e.Printf("  GitOps: %s manages (or can manage) this cluster\n", map[bool]string{true: "Argo CD", false: "Flux"}[f.ArgoCD])
	}
}

func facts(f *Facts) *Facts { return f }

func orNone(s []string) string {
	if len(s) == 0 {
		return "none yet"
	}
	return strings.Join(s, ", ")
}

// askAll asks what only a person knows, offering a way forward for everything that is missing.
func (in *Installer) askAll(ctx context.Context, e *installer.Env, a *installer.Asker, st *installed, ns string, cl Cluster) error {
	f := st.facts
	if !f.VersionOK {
		return fmt.Errorf("this cluster runs Kubernetes %s and Launchway needs %s or newer. Nothing was changed. Upgrade the cluster, or choose \"on this machine by itself\" at the first question", f.ServerVersion, MinKubeVersion)
	}
	// Rights: cluster-wide or one namespace.
	if !f.CanCreateNamespace && !(f.NamespaceExists[ns] && f.CanInstallInNS[ns]) {
		e.Printf("\nYou do not have the right to create a namespace here. Launchway needs only one namespace; ask the cluster's owner to make one for you and give you rights in it.\n")
		var err error
		ns, err = a.Ask(installer.Question{Key: "kubernetes.existing_namespace", Prompt: "The name of an existing namespace you may install into", Required: true})
		if err != nil {
			return err
		}
		f2, err := Detect(ctx, cl.Clientset(), ns)
		if err != nil {
			return err
		}
		if !f2.NamespaceExists[ns] || !f2.CanInstallInNS[ns] {
			return fmt.Errorf("you cannot install into the namespace %q (it does not exist, or you may not create deployments and secrets in it). Nothing was changed; ask the cluster's owner for rights there", ns)
		}
		st.facts = f2
		st.s.Namespace = ns
		f = f2
	}
	var err error
	// Image.
	if err = in.askImage(e, a, st); err != nil {
		return err
	}
	// High availability.
	defHA := "no"
	if f.Nodes >= 3 {
		defHA = "yes"
	}
	ha, err := a.Ask(installer.Question{Key: "kubernetes.ha", Prompt: "Run two servers for high availability?", Default: defHA, Choices: []installer.Choice{
		{Value: "no", Label: "No, one server (smaller; fine for a trial or a small team)"}, {Value: "yes", Label: "Yes, two servers (needs more room)"}}})
	if err != nil {
		return err
	}
	st.s.HA = ha == "yes"
	if err = in.askStorage(e, a, st); err != nil {
		return err
	}
	if err = in.askWayIn(e, a, st); err != nil {
		return err
	}
	pull, err := a.Ask(installer.Question{Key: "kubernetes.pull_secret", Prompt: "Name of an image pull secret to use (empty: none needed)", Default: ""})
	if err != nil {
		return err
	}
	st.s.PullSecret = pull
	if f.FreeCPUMilli < NeedCPUMilli || f.FreeMemBytes < NeedMemBytes {
		e.Printf("\nNote: there may not be enough free room in this cluster (about %d m CPU and %s memory free; a small install asks for about %d m and %s). It can still work if the nodes are not busy. If pods stay Pending, add capacity or choose one server.\n",
			f.FreeCPUMilli, quantity(f.FreeMemBytes), NeedCPUMilli, quantity(NeedMemBytes))
	}
	if f.ArgoCD || f.Flux {
		ctrl := "argocd"
		if !f.ArgoCD {
			ctrl = "flux"
		}
		what := map[string]string{"argocd": "Argo CD", "flux": "Flux"}[ctrl]
		v, err := a.Ask(installer.Question{Key: "kubernetes.gitops", Prompt: what + " manages this cluster. How should Launchway be installed?", Default: "apply", Choices: []installer.Choice{
			{Value: "apply", Label: "Apply it now from here"},
			{Value: "files", Label: "Write the files for me to commit, and apply nothing (" + what + " will apply them)"}}})
		if err != nil {
			return err
		}
		if v == "files" {
			st.gitops, st.write = ctrl, true
		}
	}
	return nil
}

func (in *Installer) askImage(e *installer.Env, a *installer.Asker, st *installed) error {
	ref := in.Image
	if ref == "" {
		ref = e.Image
	}
	if ref == "" && e.Version != "" && e.Version != "dev" {
		ref = DefaultImageRepo + ":" + e.Version
	}
	if ref == "" {
		v, err := a.Ask(installer.Question{Key: "kubernetes.image", Prompt: "The Launchway image to install (this is a development build, which has no published image)", Required: true,
			Help: "For example ghcr.io/jk-magic-creations/launchway:v1.0.0, or an image you built and loaded into the cluster."})
		if err != nil {
			return err
		}
		ref = v
	}
	st.s.ImageRepo, st.s.ImageTag, st.s.ImageDigest = ImageRefParts(ref)
	return nil
}

func (in *Installer) askStorage(e *installer.Env, a *installer.Asker, st *installed) error {
	f := st.facts
	if f.DefaultStorage != "" {
		return nil // the chart uses the cluster's default; nothing to ask
	}
	e.Printf("\nThe bundled database needs a disk, and this cluster has no default storage class.\n")
	classes := installer.Choice{Value: "class", Label: "Use a storage class I name"}
	if len(f.StorageClasses) == 0 {
		classes.Unavailable = "this cluster has no storage classes"
	}
	choice, err := a.Ask(installer.Question{Key: "kubernetes.storage", Prompt: "What should the database use?", Default: map[bool]string{true: "trial", false: "class"}[len(f.StorageClasses) == 0], Choices: []installer.Choice{
		classes,
		{Value: "trial", Label: "TRIAL: temporary storage (everything is lost if the database pod is replaced; for trying it out only)"},
		{Value: "external", Label: "My own PostgreSQL (you give its address; nothing is stored in this cluster)"},
	}})
	if err != nil {
		return err
	}
	switch choice {
	case "class":
		c, err := a.Ask(installer.Question{Key: "kubernetes.storage_class", Prompt: "Which storage class?", Default: first(f.StorageClasses), Help: "Available: " + strings.Join(f.StorageClasses, ", ")})
		if err != nil {
			return err
		}
		st.s.StorageClass = c
	case "trial":
		st.trial, st.s.TrialStorage = true, true
		e.Printf("TRIAL STORAGE: the database lives on temporary storage. Do not keep anything you care about in this installation.\n")
	case "external":
		st.extraDB, st.s.DatabaseURL = true, true
		return fmt.Errorf("using your own PostgreSQL from the installer is not built yet. Put its address in a Secret and set database.existingSecret in the values file the installer writes, or choose a storage class or the trial. Nothing was changed")
	}
	return nil
}

func (in *Installer) askWayIn(e *installer.Env, a *installer.Asker, st *installed) error {
	f := st.facts
	var cs []installer.Choice
	for _, c := range f.IngressClasses {
		cs = append(cs, installer.Choice{Value: "ingress:" + c, Label: "Publish it through the Ingress class " + c})
	}
	for _, c := range f.GatewayClasses {
		cs = append(cs, installer.Choice{Value: "gateway:" + c, Label: "Publish it through a Gateway API gateway (class " + c + ")"})
	}
	cs = append(cs, installer.Choice{Value: "forward", Label: "No public address for now: open it here with a port-forward (set up a proper address later)"})
	def := "forward"
	if len(cs) > 1 {
		def = cs[0].Value
	}
	way, err := a.Ask(installer.Question{Key: "kubernetes.way_in", Prompt: "How will people reach Launchway?", Default: def, Choices: cs})
	if err != nil {
		return err
	}
	if way == "forward" {
		st.portFwd = true
		port := "18080"
		p, err := a.Ask(installer.Question{Key: "kubernetes.local_port", Prompt: "Local port for the port-forward", Default: port})
		if err != nil {
			return err
		}
		st.s.ExternalURL = "http://127.0.0.1:" + p
		e.Printf("Later, for a real address: set ingress.* or httpRoute.* in the values file (docs: Install on Kubernetes), and a host name that points at your cluster's entrance.\n")
		return nil
	}
	host, err := a.Ask(installer.Question{Key: "kubernetes.host", Prompt: "The host name people will use (for example launchway.example.com)", Required: true,
		Help: "It must point at your cluster's entrance (a DNS record). If it does not yet, install now and make the record afterwards."})
	if err != nil {
		return err
	}
	scheme := "https"
	kind, class, _ := strings.Cut(way, ":")
	if kind == "ingress" {
		st.s.IngressClass, st.s.IngressHost = class, host
		tls, err := a.Ask(installer.Question{Key: "kubernetes.tls", Prompt: "How is the connection encrypted (TLS)?", Default: tlsDefault(f), Choices: []installer.Choice{
			{Value: "cert-manager", Label: "cert-manager makes the certificate", Unavailable: map[bool]string{true: "", false: "cert-manager is not installed in this cluster"}[f.CertManager && len(f.Issuers) > 0]},
			{Value: "secret", Label: "I have a certificate in a Secret (give its name)"},
			{Value: "none", Label: "No TLS: plain http on a private network (a warning is shown)"}}})
		if err != nil {
			return err
		}
		switch tls {
		case "cert-manager":
			iss, err := a.Ask(installer.Question{Key: "kubernetes.issuer", Prompt: "Which ClusterIssuer?", Default: first(f.Issuers), Help: "Available: " + strings.Join(f.Issuers, ", ")})
			if err != nil {
				return err
			}
			st.s.IngressAnnots = map[string]string{"cert-manager.io/cluster-issuer": iss}
			st.s.IngressTLSName = st.s.Release + "-tls"
		case "secret":
			n, err := a.Ask(installer.Question{Key: "kubernetes.tls_secret", Prompt: "The name of the TLS Secret", Required: true})
			if err != nil {
				return err
			}
			st.s.IngressTLSName = n
		default:
			scheme = "http"
			e.Printf("WARNING: plain http. Passwords and sessions cross the network unprotected. Use it on a private network only.\n")
		}
	} else {
		gw, err := a.Ask(installer.Question{Key: "kubernetes.gateway", Prompt: "Which Gateway should it attach to (NAME or NAMESPACE/NAME)?", Required: true})
		if err != nil {
			return err
		}
		ref := &GatewayRef{Name: gw}
		if n, nm, ok := strings.Cut(gw, "/"); ok {
			ref.Namespace, ref.Name = n, nm
		}
		st.s.Gateway, st.s.GatewayHost = ref, host
		https, err := a.Confirm("kubernetes.gateway_https", "Does that Gateway serve this host over https?", true)
		if err != nil {
			return err
		}
		if !https {
			scheme = "http"
			e.Printf("WARNING: plain http. Use it on a private network only.\n")
		}
	}
	st.s.ExternalURL = scheme + "://" + host
	return nil
}

func tlsDefault(f *Facts) string {
	if f.CertManager && len(f.Issuers) > 0 {
		return "cert-manager"
	}
	return "secret"
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// execute shows the summary, then writes, applies and waits.
func (in *Installer) execute(ctx context.Context, e *installer.Env, a *installer.Asker, o installer.Options, t *installer.Transcript, cl Cluster, st *installed, res installer.Result) (installer.Result, error) {
	s := st.s
	chart, err := LoadChart()
	if err != nil {
		return res, err
	}
	valuesPath, err := a.Ask(installer.Question{Key: "kubernetes.values_file", Prompt: "Where should I write the values file?", Default: "launchway-values.yaml"})
	if err != nil {
		return res, err
	}
	st.values = valuesPath
	rendered, err := Render(chart, s.Values(), s.Release, s.Namespace, Caps{KubeVersion: st.facts.ServerVersion, APIVersions: st.facts.APIVersions})
	if err != nil {
		return res, err
	}
	if st.trial {
		trialStorage(rendered)
	}
	vb, err := s.YAML()
	if err != nil {
		return res, err
	}
	plan := installer.Plan{Keeps: []string{
		"anything else in cluster " + cl.Name() + ": only objects in namespace " + s.Namespace + " are created or changed",
		"the secrets (root key, database password, setup code) are made once in the cluster and kept; running this again never regenerates them"}}
	plan.Steps = append(plan.Steps, installer.Step{Title: "Write the values file " + valuesPath + " (" + strconv.Itoa(strings.Count(string(vb), "\n")) + " lines, no secrets)",
		Do: func(context.Context) error { return os.WriteFile(valuesPath, vb, 0o644) }})
	if st.write {
		gf, err := WriteGitOps(s, st.gitops, "main")
		if err != nil {
			return res, err
		}
		dir := filepath.Dir(valuesPath)
		plan.Steps = append(plan.Steps, installer.Step{Title: "Write " + gf.Name + " for " + st.gitops + " (nothing is applied to the cluster)", Do: func(context.Context) error {
			return os.WriteFile(filepath.Join(dir, gf.Name), []byte(gf.Manifest), 0o644)
		}})
		plan.Summarize(e, "Here is what I am about to do:")
		res.Steps = len(plan.Steps)
		ok, err := installerGoAhead(a, o)
		if err != nil || !ok {
			return res, err
		}
		if err := plan.Execute(ctx, e, t); err != nil {
			return res, err
		}
		e.Printf("\nCommit %s and %s to your GitOps repository; %s applies them. The setup code appears in the cluster once Launchway is running:\n  kubectl -n %s get secret -o name | grep setup-code\n",
			valuesPath, gf.Name, st.gitops, s.Namespace)
		return res, nil
	}
	chartRef := "./deploy/helm/launchway"
	if o.Bundle == "" {
		e.Printf("\nThe same thing with Helm, if you prefer to run it yourself:\n  %s\n", s.HelmCommand(valuesPath, chartRef))
	}
	var ap Applier = cl
	waiter := &Waiter{CS: cl.Clientset(), Out: e.Out, Poll: in.Poll, Sleep: in.Sleep, Stuck: 0}
	plan.Steps = append(plan.Steps, installer.Step{Title: "Make the namespace " + s.Namespace + " if it does not exist", Do: func(ctx context.Context) error {
		_, err := cl.Clientset().CoreV1().Namespaces().Get(ctx, s.Namespace, metav1.GetOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": s.Namespace}}}
		return ap.Apply(ctx, ns)
	}})
	plan.Steps = append(plan.Steps, installer.Step{Title: "Run the setup jobs that make the root key, the database password and the setup code once (kept if they exist)", Do: func(ctx context.Context) error {
		for _, h := range rendered.Hooks("pre-install") {
			if err := in.applyHook(ctx, ap, waiter, h); err != nil {
				return err
			}
		}
		return nil
	}})
	body := rendered.Body()
	plan.Steps = append(plan.Steps, installer.Step{Title: fmt.Sprintf("Create %d Launchway objects in %s (server, runner, database, network policies)", len(body), s.Namespace), Do: func(ctx context.Context) error {
		for _, o := range body {
			stampHelm(o.Obj, s.Release, s.Namespace)
			if err := ap.Apply(ctx, o.Obj); err != nil {
				return err
			}
		}
		return nil
	}})
	var deployNames []string
	for _, o := range body {
		if o.Obj.GetKind() == "Deployment" || o.Obj.GetKind() == "StatefulSet" {
			deployNames = append(deployNames, o.Obj.GetName())
		}
	}
	limit := in.Limit
	if limit == 0 {
		limit = 8 * time.Minute
	}
	plan.Steps = append(plan.Steps, installer.Step{Title: "Wait until Launchway is running (up to " + limit.String() + ")", Do: func(ctx context.Context) error {
		return in.waitRetry(ctx, e, a, ap, waiter, body, st, deployNames, limit)
	}})
	plan.Summarize(e, "Here is what I am about to do:")
	res.Steps = len(plan.Steps)
	ok, err := installerGoAhead(a, o)
	if err != nil || !ok {
		if o.DryRun {
			e.Printf("\nDry run: nothing was changed. The values file would hold:\n\n%s\n", vb)
		}
		return res, err
	}
	if err := plan.Execute(ctx, e, t); err != nil {
		return res, err
	}
	return in.finish(ctx, e, a, o, t, cl, st, res)
}

func installerGoAhead(a *installer.Asker, o installer.Options) (bool, error) {
	if o.DryRun {
		return false, nil
	}
	if o.Assume {
		return true, nil
	}
	return a.Confirm("proceed", "Go ahead with this?", true)
}

// waitRetry waits for the deployments; when pods cannot pull their image it offers the way forward and tries again.
func (in *Installer) waitRetry(ctx context.Context, e *installer.Env, a *installer.Asker, ap Applier, w *Waiter, body []Object, st *installed, names []string, limit time.Duration) error {
	err := w.WaitDeployments(ctx, st.s.Namespace, names, limit)
	se, ok := err.(*StuckError)
	if !ok || err == nil {
		return err
	}
	for _, p := range se.Problems {
		if p.Kind == "image-pull" {
			e.Printf("\n%s\n", se.Error())
			if !e.Interactive {
				return err
			}
			sec, aerr := a.Ask(installer.Question{Key: "kubernetes.retry_pull_secret", Prompt: "Name of an existing image pull secret to use now (empty: stop here)", Default: ""})
			if aerr != nil || sec == "" {
				return err
			}
			st.s.PullSecret = sec
			chart, cerr := LoadChart()
			if cerr != nil {
				return cerr
			}
			r, rerr := Render(chart, st.s.Values(), st.s.Release, st.s.Namespace, Caps{KubeVersion: st.facts.ServerVersion, APIVersions: st.facts.APIVersions})
			if rerr != nil {
				return rerr
			}
			if st.trial {
				trialStorage(r)
			}
			for _, o := range r.Body() {
				stampHelm(o.Obj, st.s.Release, st.s.Namespace)
				if aerr := ap.Apply(ctx, o.Obj); aerr != nil {
					return aerr
				}
			}
			return w.WaitDeployments(ctx, st.s.Namespace, names, limit)
		}
	}
	return err
}

// applyHook applies one hook object, recreating it first when the chart says so, and waits for a Job.
func (in *Installer) applyHook(ctx context.Context, ap Applier, w *Waiter, h Object) error {
	for _, d := range h.Delete {
		if d == "before-hook-creation" {
			if err := ap.Remove(ctx, h.Obj); err != nil {
				return err
			}
			break
		}
	}
	if err := ap.Apply(ctx, h.Obj); err != nil {
		return err
	}
	if h.Obj.GetKind() == "Job" {
		if err := w.WaitJob(ctx, h.Obj.GetNamespace(), h.Obj.GetName(), 3*time.Minute); err != nil {
			return err
		}
		for _, d := range h.Delete {
			if d == "hook-succeeded" {
				_ = ap.Remove(ctx, h.Obj)
			}
		}
	}
	return nil
}

// stampHelm marks an object as part of a Helm release, so a later `helm upgrade` adopts it.
func stampHelm(o *unstructured.Unstructured, release, ns string) {
	l := o.GetLabels()
	if l == nil {
		l = map[string]string{}
	}
	l["app.kubernetes.io/managed-by"] = "Helm"
	o.SetLabels(l)
	an := o.GetAnnotations()
	if an == nil {
		an = map[string]string{}
	}
	an["meta.helm.sh/release-name"] = release
	an["meta.helm.sh/release-namespace"] = ns
	o.SetAnnotations(an)
}

// trialStorage replaces the bundled database's volume with temporary storage.
func trialStorage(r *Rendered) {
	var kept []Object
	for _, o := range r.Objects {
		if o.Obj.GetKind() == "PersistentVolumeClaim" {
			continue
		}
		if o.Obj.GetKind() == "Deployment" || o.Obj.GetKind() == "StatefulSet" {
			vols, found, _ := unstructured.NestedSlice(o.Obj.Object, "spec", "template", "spec", "volumes")
			if found {
				for i, v := range vols {
					m, _ := v.(map[string]any)
					if _, ok := m["persistentVolumeClaim"]; ok {
						vols[i] = map[string]any{"name": m["name"], "emptyDir": map[string]any{}}
					}
				}
				_ = unstructured.SetNestedSlice(o.Obj.Object, vols, "spec", "template", "spec", "volumes")
			}
		}
		kept = append(kept, o)
	}
	r.Objects = kept
}

var _ = corev1.Secret{}
