package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestKubePodGivesTheStepUserAnAccountEntry(t *testing.T) {
	k := testKube(t, KubernetesOptions{})
	pod, _ := k.podSpec("p", "run_1", podTestSpecs)

	// One init container for the one step container, in its own image and under the same restrictions.
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("init containers %+v", pod.Spec.InitContainers)
	}
	init := pod.Spec.InitContainers[0]
	if init.Name != "user-python" || init.Image != "docker.io/library/python:3.12" {
		t.Errorf("init container %s in %s", init.Name, init.Image)
	}
	sc := init.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("init security context %+v", sc)
	}
	if init.Command[0] != "sh" || init.Command[len(init.Command)-4] != "1000" || init.Command[len(init.Command)-3] != "1000" ||
		init.Command[len(init.Command)-2] != stepUserName || init.Command[len(init.Command)-1] != stepHome {
		t.Errorf("init command arguments %v", init.Command[3:])
	}

	// The step container mounts the two files read-only over /etc/passwd and /etc/group; the service and the helper do not.
	byName := map[string]corev1.Container{}
	for _, c := range pod.Spec.Containers {
		byName[c.Name] = c
	}
	var etc []corev1.VolumeMount
	for _, m := range byName["python"].VolumeMounts {
		if strings.HasPrefix(m.MountPath, "/etc/") {
			etc = append(etc, m)
		}
	}
	if len(etc) != 2 || !etc[0].ReadOnly || !etc[1].ReadOnly || etc[0].SubPath != "python/passwd" || etc[1].MountPath != "/etc/group" || etc[1].SubPath != "python/group" {
		t.Errorf("step container /etc mounts %+v", etc)
	}
	for _, name := range []string{"db", helperName} {
		if _, has := mountsOf(byName[name])["/etc/passwd"]; has {
			t.Errorf("%s must keep its own /etc/passwd", name)
		}
	}
	env := envOf(byName["python"])
	if env["USER"] != "launchway" || env["LOGNAME"] != "launchway" || env["HOME"] != stepHome {
		t.Errorf("user environment %v", env)
	}
	if e := envOf(byName["db"]); e["USER"] != "" {
		t.Errorf("a service keeps its own settings: %v", e)
	}
	// The security settings are exactly as before: not root, no escalation, no capabilities.
	if ps := pod.Spec.SecurityContext; ps == nil || *ps.RunAsNonRoot != true || *ps.RunAsUser != 1000 {
		t.Errorf("pod security %+v", ps)
	}
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == userFilesVolume {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.EmptyDir == nil || vol.EmptyDir.Medium != corev1.StorageMediumMemory {
		t.Errorf("the account files must be an in-memory emptyDir: %+v", vol)
	}
}

func TestKubePodUserEntryCanBeTurnedOff(t *testing.T) {
	off := false
	for name, o := range map[string]KubernetesOptions{"user_entry false": {UserEntry: &off}, "unrestricted": {Restricted: &off}} {
		pod, _ := testKube(t, o).podSpec("p", "run_1", podTestSpecs)
		if len(pod.Spec.InitContainers) != 0 {
			t.Errorf("%s: init containers %+v", name, pod.Spec.InitContainers)
		}
		for _, c := range pod.Spec.Containers {
			if _, has := mountsOf(c)["/etc/passwd"]; has || envOf(c)["USER"] == "launchway" {
				t.Errorf("%s: %s has the account entry", name, c.Name)
			}
		}
	}
}

func TestKubeStepSaysWhoItRunsAs(t *testing.T) {
	off := false
	for _, c := range []struct {
		o    KubernetesOptions
		want string
	}{
		{KubernetesOptions{}, "Running as launchway (uid 1000, gid 1000), HOME=/home/launchway"},
		{KubernetesOptions{RunAsUser: ptr(int64(2000)), RunAsGroup: ptr(int64(3000))}, "Running as launchway (uid 2000, gid 3000)"},
		{KubernetesOptions{UserEntry: &off}, "no account entry (kubernetes.user_entry: false)"},
		{KubernetesOptions{Restricted: &off}, "Running as the image's own user"},
	} {
		if got := testKube(t, c.o).stepUserLine(); !strings.Contains(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.o, got, c.want)
		}
	}
}

func TestKubeUserInitProblemIsExplained(t *testing.T) {
	p := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{Name: "user-x", Image: "gcr.io/distroless/static",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerError", Message: "exec: \"sh\": executable file not found"}}}}}}
	if got := userInitProblem(p); !strings.Contains(got, "distroless/static") || !strings.Contains(got, "user_entry: false") {
		t.Errorf("%q", got)
	}
	p.Status.InitContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}
	if got := userInitProblem(p); !strings.Contains(got, "exit 1") {
		t.Errorf("%q", got)
	}
	p.Status.InitContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	if got := userInitProblem(p); got != "" {
		t.Errorf("a finished init container is no problem: %q", got)
	}
}

// runUserScript runs the init container's script on the host's sh, over the given passwd and group files.
func runUserScript(t *testing.T, passwd, group *string, uid, gid string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	env := os.Environ()
	write := func(name string, content *string, key string) {
		if content == nil {
			env = append(env, key+"="+filepath.Join(dir, "missing-"+name))
			return
		}
		p := filepath.Join(dir, "in-"+name)
		if err := os.WriteFile(p, []byte(*content), 0o644); err != nil {
			t.Fatal(err)
		}
		env = append(env, key+"="+p)
	}
	write("passwd", passwd, "PASSWD_IN")
	write("group", group, "GROUP_IN")
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", userScript, "sh", out, uid, gid, stepUserName, stepHome)
	cmd.Env = env
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script failed: %v\n%s", err, b)
	}
	p, _ := os.ReadFile(filepath.Join(out, "passwd"))
	g, _ := os.ReadFile(filepath.Join(out, "group"))
	return string(p), string(g)
}

func sp(s string) *string { return &s }

// getpwuid is the lookup tools do: the first line of the passwd file with this uid.
func getpwuid(passwd, uid string) (name, home, shell string, ok bool) {
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(line, ":")
		if len(f) == 7 && f[2] == uid {
			return f[0], f[5], f[6], true
		}
	}
	return "", "", "", false
}

// This is the regression test for "No user exists for uid 1000": the user lookup the ssh client does finds an account
// for the run's uid, whatever the image's own files hold.
func TestUserScriptMakesTheStepUserResolvable(t *testing.T) {
	debian := "root:x:0:0:root:/root:/bin/bash\ndaemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\nnobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\n"
	debianG := "root:x:0:\ndaemon:x:1:\nnogroup:x:65534:\n"
	cases := []struct {
		name           string
		passwd, group  *string
		keepsFromImage []string
	}{
		{"an image that does not know uid 1000", &debian, &debianG, []string{"daemon:x:1:1", "nobody:x:65534"}},
		{"an image that has a user with that uid", sp("root:x:0:0:root:/root:/bin/sh\nnode:x:1000:1000::/home/node:/bin/bash\n"), sp("root:x:0:\nnode:x:1000:\n"), []string{"root:x:0:0"}},
		{"an image with no passwd or group file (distroless-like)", nil, nil, nil},
		{"an image with an empty file", sp(""), sp(""), nil},
		{"a file without a final newline", sp("root:x:0:0:root:/root:/bin/sh"), sp("root:x:0:"), []string{"root:x:0:0"}},
		{"an image that already has a launchway user with another uid", sp("root:x:0:0:r:/root:/bin/sh\nlaunchway:x:5:5::/x:/bin/sh\n"), sp("launchway:x:5:\n"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pw, gr := runUserScript(t, c.passwd, c.group, "1000", "1000")
			name, home, shell, ok := getpwuid(pw, "1000")
			if !ok || name != "launchway" || home != "/home/launchway" || shell != "/sbin/nologin" {
				t.Fatalf("lookup of uid 1000: %q %q %q %v in:\n%s", name, home, shell, ok, pw)
			}
			if strings.Count(pw, "\n1000:")+strings.Count(pw, ":x:1000:") != 1 {
				t.Errorf("uid 1000 must be in the file exactly once:\n%s", pw)
			}
			if n, _, _, _ := getpwuid(pw, "0"); n != "root" {
				t.Errorf("root must still resolve:\n%s", pw)
			}
			if !strings.Contains(gr, "launchway:x:1000:") || !strings.Contains(gr, "root:x:0:") || strings.Count(gr, ":x:1000:") != 1 {
				t.Errorf("group file:\n%s", gr)
			}
			if strings.Contains(pw, "launchway:x:5:") || strings.Contains(gr, "launchway:x:5:") {
				t.Errorf("an old entry named launchway with another id is replaced:\n%s\n%s", pw, gr)
			}
			for _, keep := range c.keepsFromImage {
				if !strings.Contains(pw, keep) {
					t.Errorf("the image's own %q is kept:\n%s", keep, pw)
				}
			}
		})
	}
}

func TestUserScriptUsesTheRunsUidAndGid(t *testing.T) {
	pw, gr := runUserScript(t, sp("root:x:0:0:r:/root:/bin/sh\n"), sp("root:x:0:\n"), "4242", "777")
	if n, _, _, ok := getpwuid(pw, "4242"); !ok || n != "launchway" {
		t.Errorf("passwd:\n%s", pw)
	}
	if !strings.Contains(pw, "launchway:x:4242:777:") || !strings.Contains(gr, "launchway:x:777:") {
		t.Errorf("passwd:\n%s\ngroup:\n%s", pw, gr)
	}
}
