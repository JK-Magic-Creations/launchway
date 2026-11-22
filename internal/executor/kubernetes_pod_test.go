package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// These tests check the pod the Kubernetes executor asks for; they need no
// cluster. The cluster tests (kubernetes_test.go) check that it is admitted
// and works under Pod Security "restricted".

func testKube(t *testing.T, o KubernetesOptions) *Kubernetes {
	t.Helper()
	if err := o.setDefaults(); err != nil {
		t.Fatal(err)
	}
	return &Kubernetes{o: o, owner: "owner"}
}

var podTestSpecs = []ContainerSpec{
	{Name: "python", Image: "docker.io/library/python:3.12"},
	{Name: "db", Image: "docker.io/library/postgres:17", Service: true,
		Env: map[string]string{"HOME": "/var/lib/postgresql", "POSTGRES_PASSWORD": "pw"}},
}

func envOf(c corev1.Container) map[string]string {
	m := map[string]string{}
	for _, e := range c.Env {
		if _, dup := m[e.Name]; dup {
			m[e.Name] = "DUPLICATE"
			continue
		}
		m[e.Name] = e.Value
	}
	return m
}

func mountsOf(c corev1.Container) map[string]string {
	m := map[string]string{}
	for _, v := range c.VolumeMounts {
		m[v.MountPath] = v.Name
	}
	return m
}

func TestKubePodRestrictedByDefault(t *testing.T) {
	k := testKube(t, KubernetesOptions{})
	pod, steps := k.podSpec("p", "run_1", podTestSpecs)
	if steps["python"] != "python" || steps["db"] != "" {
		t.Fatalf("step containers %v", steps)
	}

	ps := pod.Spec.SecurityContext
	if ps == nil || ps.RunAsNonRoot == nil || !*ps.RunAsNonRoot || *ps.RunAsUser != 1000 || *ps.RunAsGroup != 1000 ||
		*ps.FSGroup != 1000 || ps.SeccompProfile == nil || ps.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("pod security context %+v", ps)
	}

	vols := map[string]*corev1.EmptyDirVolumeSource{}
	for _, v := range pod.Spec.Volumes {
		if v.EmptyDir == nil {
			t.Fatalf("volume %s is not an emptyDir: restricted allows few volume types", v.Name)
		}
		vols[v.Name] = v.EmptyDir
	}
	for _, name := range []string{"workspace", "launchway", "home", "tmp"} {
		if vols[name] == nil {
			t.Fatalf("no %s volume in %v", name, pod.Spec.Volumes)
		}
	}

	if len(pod.Spec.Containers) != 3 || pod.Spec.Containers[0].Name != helperName {
		t.Fatalf("containers %+v", pod.Spec.Containers)
	}
	for _, c := range pod.Spec.Containers {
		sc := c.SecurityContext
		if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
			sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) != 0 {
			t.Fatalf("container %s: security context %+v", c.Name, sc)
		}
		if sc.Privileged != nil || sc.RunAsUser != nil || sc.RunAsNonRoot != nil {
			t.Fatalf("container %s should take the user from the pod: %+v", c.Name, sc)
		}
		m := mountsOf(c)
		if m["/workspace"] != "workspace" || m["/launchway"] != "launchway" || m[stepHomeVolume] != "home" || m["/tmp"] != "tmp" {
			t.Fatalf("container %s: mounts %v", c.Name, m)
		}
		env := envOf(c)
		if c.Name == "db" {
			// The service's own settings win, and are not repeated.
			if env["HOME"] != "/var/lib/postgresql" || env["POSTGRES_PASSWORD"] != "pw" {
				t.Fatalf("service env %v", env)
			}
		} else if env["HOME"] != stepHome {
			t.Fatalf("container %s: HOME %q", c.Name, env["HOME"])
		}
		for _, name := range []string{"PIP_CACHE_DIR", "npm_config_cache", "GOCACHE"} {
			if !strings.HasPrefix(env[name], stepHome+"/") {
				t.Fatalf("container %s: %s=%q should be under %s", c.Name, name, env[name], stepHome)
			}
		}
	}
}

// A step's own environment is exported by the script that runs it, after
// the container started with the pod's defaults, so it wins over them.
func TestKubeStepEnvComesAfterThePodDefaults(t *testing.T) {
	k := testKube(t, KubernetesOptions{})
	pod, _ := k.podSpec("p", "run_1", podTestSpecs)
	if env := envOf(pod.Spec.Containers[1]); env["HOME"] != stepHome {
		t.Fatalf("env %v", env)
	}
	// Run sources the env file after the container's environment is set:
	// see the script in Run. This guards the order.
	if !strings.Contains(stepScript("/launchway/s"), "cd /workspace; . /launchway/s/env; exec sh -e") {
		t.Fatalf("the step's env file must be sourced just before the script: %s", stepScript("/launchway/s"))
	}
}

func TestKubePodOtherUser(t *testing.T) {
	k := testKube(t, KubernetesOptions{RunAsUser: ptr(int64(2000)), RunAsGroup: ptr(int64(0))})
	pod, _ := k.podSpec("p", "run_1", podTestSpecs)
	ps := pod.Spec.SecurityContext
	if *ps.RunAsUser != 2000 || *ps.RunAsGroup != 0 || *ps.FSGroup != 0 || !*ps.RunAsNonRoot {
		t.Fatalf("pod security context %+v", ps)
	}
}

// With restricted: false the pod is what it was before there was a choice:
// no security context, the images' own users, no extra volumes or variables.
func TestKubePodUnrestricted(t *testing.T) {
	k := testKube(t, KubernetesOptions{Restricted: ptr(false)})
	pod, _ := k.podSpec("p", "run_1", podTestSpecs)
	if pod.Spec.SecurityContext != nil {
		t.Fatalf("pod security context %+v", pod.Spec.SecurityContext)
	}
	if len(pod.Spec.Volumes) != 2 {
		t.Fatalf("volumes %+v", pod.Spec.Volumes)
	}
	for _, c := range pod.Spec.Containers {
		if c.SecurityContext != nil || len(c.VolumeMounts) != 2 {
			t.Fatalf("container %s: %+v", c.Name, c)
		}
		if env := envOf(c); env["PIP_CACHE_DIR"] != "" || (c.Name != "db" && env["HOME"] != "") {
			t.Fatalf("container %s env %v", c.Name, env)
		}
	}
}

func TestKubeSecuritySettingsChecked(t *testing.T) {
	for _, c := range []struct {
		o    KubernetesOptions
		want string
	}{
		{KubernetesOptions{RunAsUser: ptr(int64(0))}, "other than root"},
		{KubernetesOptions{RunAsUser: ptr(int64(-1))}, "other than root"},
		{KubernetesOptions{RunAsGroup: ptr(int64(-5))}, "not a group"},
		{KubernetesOptions{Restricted: ptr(false), RunAsUser: ptr(int64(1000))}, "apply only when restricted"},
		{KubernetesOptions{Restricted: ptr(false), RunAsGroup: ptr(int64(1000))}, "apply only when restricted"},
	} {
		err := c.o.setDefaults()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err %v, want %q", c.o, err, c.want)
		}
	}
	o := KubernetesOptions{Restricted: ptr(true)}
	if err := o.setDefaults(); err != nil || *o.RunAsUser != 1000 || *o.RunAsGroup != 1000 {
		t.Fatalf("restricted: true: %+v %v", o, err)
	}
}

// The step user's home is a directory of its own, private to it: the volume under it is writable by everyone, which
// tools such as platformdirs refuse as a home ("writable by another user").
func TestHomeSetupMakesAPrivateHomeOwnedByTheStepUser(t *testing.T) {
	if stepHome == stepHomeVolume || !strings.HasPrefix(stepHome, stepHomeVolume+"/") {
		t.Fatalf("HOME %s must be a directory inside the volume %s, not the volume itself", stepHome, stepHomeVolume)
	}
	vol := t.TempDir()
	if err := os.Chmod(vol, 0o777); err != nil { // what Kubernetes gives an emptyDir
		t.Fatal(err)
	}
	home := filepath.Join(vol, "user")
	for i := 0; i < 2; i++ { // and a second pass changes nothing
		if out, err := exec.Command("sh", "-c", homeSetup(home)).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	for _, d := range []string{home, home + "/.cache", home + "/.config", home + "/.local/share"} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %o, want 0700", d, fi.Mode().Perm())
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
			t.Errorf("%s is owned by another user than the one that made it", d)
		}
	}
}

func TestStepPodEnvironmentHasSaneHomeAndXDGDirectories(t *testing.T) {
	env := map[string]string{}
	for _, e := range homeEnv {
		env[e.Name] = e.Value
	}
	for k, want := range map[string]string{"HOME": stepHome, "XDG_CACHE_HOME": stepHome + "/.cache", "XDG_CONFIG_HOME": stepHome + "/.config", "XDG_DATA_HOME": stepHome + "/.local/share"} {
		if env[k] != want {
			t.Errorf("%s=%q, want %q", k, env[k], want)
		}
	}
}
