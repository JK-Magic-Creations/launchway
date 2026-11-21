package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jk-magic-creations/launchway/internal/installer"
)

func (in *Installer) serverSelector(release string) string {
	return "app.kubernetes.io/instance=" + release + ",app.kubernetes.io/component=server"
}

func (in *Installer) forwardFn(cl Cluster) func(ctx context.Context, c Cluster, ns string, local, remote int, out io.Writer) (func(), error) {
	if in.Forward != nil {
		return in.Forward
	}
	return func(ctx context.Context, c Cluster, ns string, local, remote int, out io.Writer) (func(), error) {
		return Forward(ctx, c.RESTConfig(), c.Clientset(), ns, in.serverSelector(in.release(c)), local, remote, out)
	}
}

func (in *Installer) release(Cluster) string { return in.lastRelease }

func (in *Installer) setupDone(ctx context.Context, base string) bool {
	if in.SetupDone != nil {
		return in.SetupDone(ctx, base)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/setup", nil)
	r, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var v struct {
		Required bool `json:"required"`
	}
	if json.NewDecoder(r.Body).Decode(&v) != nil {
		return false
	}
	return !v.Required
}

func reachable(ctx context.Context, base string) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/setup", nil)
	r, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	r.Body.Close()
	return r.StatusCode < 500
}

// finish reads the one-time setup code, makes the address usable from this machine and ends where the
// browser takes over.
func (in *Installer) finish(ctx context.Context, e *installer.Env, a *installer.Asker, o installer.Options, t *installer.Transcript, cl Cluster, st *installed, res installer.Result) (installer.Result, error) {
	s := st.s
	in.lastRelease = s.Release
	code := ""
	secrets, err := cl.Clientset().CoreV1().Secrets(s.Namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, sec := range secrets.Items {
			if strings.HasSuffix(sec.Name, "-setup-code") {
				code = strings.TrimSpace(string(sec.Data["code"]))
			}
		}
	}
	t.Hide(code)
	addr := s.ExternalURL
	res.Address = addr
	e.Printf("\nLaunchway is running in namespace %s.\n", s.Namespace)
	stop := func() {}
	forwarded := false
	// Is the address reachable from this machine? If not, offer a port-forward kept open until setup is done.
	if !st.portFwd && !reachable(ctx, addr) {
		e.Printf("The address %s does not answer from this machine yet (a DNS record or the entrance may still be missing).\n", addr)
		st.portFwd = true
		u, _ := url.Parse(addr)
		_ = u
		addr = "http://127.0.0.1:" + strconv.Itoa(freeLocalPort(18080))
	}
	if st.portFwd {
		local := 18080
		if u, err := url.Parse(addr); err == nil {
			if _, p, err := net.SplitHostPort(u.Host); err == nil {
				local, _ = strconv.Atoi(p)
			}
		}
		keep, err := a.Confirm("kubernetes.keep_forward", fmt.Sprintf("Open a port-forward to Launchway on %s and keep it open until the first administrator is made?", fmt.Sprintf("http://127.0.0.1:%d", local)), e.Interactive)
		if err != nil {
			return res, err
		}
		if keep {
			stp, err := in.forwardFn(cl)(ctx, cl, s.Namespace, local, 8080, e.Out)
			if err != nil {
				e.Printf("The port-forward did not start: %v\nRun it yourself:  kubectl -n %s port-forward svc/%s-launchway %d:8080\n", err, s.Namespace, s.Release, local)
			} else {
				stop, forwarded = stp, true
				addr = fmt.Sprintf("http://127.0.0.1:%d", local)
				res.Address = addr
			}
		} else {
			e.Printf("When you want it:  kubectl -n %s port-forward svc/<the launchway service> %d:8080\n", s.Namespace, local)
		}
	}
	e.Printf("Address: %s\n", addr)
	if code != "" {
		e.Printf("Open it and enter this one-time setup code to make the first administrator (valid 24 hours, works once):\n\n    %s\n\n", code)
		e.Printf("Lost it or expired?  kubectl -n %s exec deploy/%s-launchway-server -- launchway users setup-code\n", s.Namespace, s.Release)
	} else {
		e.Printf("The setup code was not found in the cluster (the first administrator may exist already). If sign-in asks for one:  kubectl -n %s exec deploy/<server> -- launchway users setup-code\n", s.Namespace)
	}
	e.Printf("KEEP the root key: the Secret ending in -root-key in namespace %s is not removed by uninstall and is the only way to read the installation's stored secrets. Copy it somewhere safe.\n", s.Namespace)
	facts := map[string]string{"sign_in": "accounts", "tls": map[bool]string{true: "yes", false: "no"}[strings.HasPrefix(s.ExternalURL, "https://")],
		"network_policies": map[Tri]string{Yes: "enforced", No: "not-enforced", Unknown: "unknown"}[st.facts.NetpolEnforced]}
	if st.portFwd {
		facts["tls"], facts["loopback"] = "loopback", "yes"
	}
	if o.Doctor != nil {
		e.Printf("\nA plain security check of what you just installed:\n")
		o.Doctor(ctx, e.Out, installer.DoctorInput{URL: addr, Kubernetes: true, Facts: facts})
	}
	if !o.NoBrowser && e.OpenBrowser != nil {
		if yes, _ := a.Confirm("open_browser", "Open "+addr+" in your browser now?", e.Interactive); yes {
			if err := e.OpenBrowser(addr); err != nil {
				e.Printf("Could not open a browser from here (%v). Open %s yourself.\n", err, addr)
			}
		}
	}
	if forwarded {
		e.Printf("\nThe port-forward stays open until the first administrator is made (or until you press Ctrl-C).\n")
		for !in.setupDone(ctx, addr) {
			select {
			case <-ctx.Done():
				stop()
				return res, nil
			case <-time.After(2 * time.Second):
			}
		}
		e.Printf("Setup is complete. Closing the port-forward. Next time:  kubectl -n %s port-forward svc/%s-launchway 8080:8080\n", s.Namespace, s.Release)
		stop()
	}
	return res, nil
}

// uninstall removes what the chart made, and says what it keeps.
func (in *Installer) uninstall(ctx context.Context, e *installer.Env, a *installer.Asker, o installer.Options, cl Cluster, ns, release string, res installer.Result) (installer.Result, error) {
	rm, kept, err := in.inventory(ctx, cl, ns, release)
	if err != nil {
		return res, err
	}
	e.Printf("\nHere is what I would remove from namespace %s of %s:\n", ns, cl.Name())
	for _, r := range rm {
		e.Printf("  - %s\n", r)
	}
	if len(rm) == 0 {
		e.Printf("  (nothing: no objects of release %s were found)\n", release)
	}
	e.Printf("What stays:\n")
	e.Printf("  - the volume of the bundled database, and everything in it\n")
	e.Printf("  - every Secret, including the ROOT KEY (without it stored secrets cannot be read), the database password and the runner token\n")
	e.Printf("  - the namespace itself\n")
	for _, k := range kept {
		e.Printf("  - %s\n", k)
	}
	if o.DryRun {
		e.Printf("\nDry run: nothing was removed.\n")
		return res, nil
	}
	if !o.Assume {
		ok, err := a.Confirm("proceed", "Remove these now?", false)
		if err != nil || !ok {
			return res, err
		}
	}
	if err := in.remove(ctx, cl, ns, release); err != nil {
		return res, err
	}
	e.Printf("\nRemoved. To delete the data as well (cannot be undone), and only if you will never need it again:\n")
	c := cl.Clientset()
	if l, err := c.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + release}); err == nil {
		for _, p := range l.Items {
			e.Printf("  kubectl -n %s delete pvc %s\n", ns, p.Name)
		}
	}
	if l, err := c.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{}); err == nil {
		for _, sec := range l.Items {
			if strings.HasPrefix(sec.Name, release) {
				e.Printf("  kubectl -n %s delete secret %s\n", ns, sec.Name)
			}
		}
	}
	return res, nil
}
