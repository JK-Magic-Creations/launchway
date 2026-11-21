package executor

import (
	_ "embed"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

// A step runs as a numeric user (kubernetes.run_as_user, 1000 by default) that most images know nothing about. Tools
// that look their user up then fail: the OpenSSH client prints "No user exists for uid 1000" and refuses to run, git
// over ssh with it, and some language runtimes and tools that read $HOME from the account do the same. So, unless the
// operator turns it off (kubernetes.user_entry: false), each step container gets an /etc/passwd and /etc/group that
// know the user:
//
//   - an init container in the step's own image (same security context, so the same checks apply) copies the image's
//     /etc/passwd and /etc/group, without any entry for the run's uid or gid, to a small in-memory volume, and adds
//     "launchway" for them, with the step's HOME and the shell /sbin/nologin;
//   - the step container mounts those two files read-only over /etc/passwd and /etc/group.
//
// The init container needs only /bin/sh (a step image needs it anyway: steps run with sh) and works with an image that
// has no /etc/passwd or /etc/group. Nothing in the image is changed, and nothing about the security context is relaxed:
// the user is still not root, and the root filesystem can be read-only because the two files are mounts.

// stepUserName is the account name of the step's user.
const stepUserName = "launchway"

// userFilesVolume holds, for each step container, its passwd and group files.
const userFilesVolume = "userfiles"

// userEntry reports whether step containers get a passwd and group entry for their user.
func (o KubernetesOptions) userEntry() bool {
	return o.restricted() && (o.UserEntry == nil || *o.UserEntry)
}

// userEnv is set in the step containers of a pod that has the entry, so that USER and LOGNAME say what the account
// database says (HOME is already stepHome).
var userEnv = []corev1.EnvVar{{Name: "USER", Value: stepUserName}, {Name: "LOGNAME", Value: stepUserName}}

// userScript builds <dir>/passwd and <dir>/group; see user_entry.sh. It uses only the shell's own commands, so it works
// in an image with nothing else in it. PASSWD_IN and GROUP_IN name the files to start from (the image's own, by
// default); the tests set them. scripts/ci/test-like-ci.sh runs the same file.
//
//go:embed user_entry.sh
var userScript string

// userInit is the init container that makes the passwd and group files for one step container.
func (k *Kubernetes) userInit(sp ContainerSpec) corev1.Container {
	return corev1.Container{
		Name:  "user-" + sp.Name,
		Image: sp.Image,
		// "sh" is $0; the script's arguments follow.
		// The files go in the volume's root, named after the container: a directory per container would have to be
		// made first, and nothing in the pod can make it.
		Command: []string{"sh", "-c", userScript, "sh", "/launchway-user",
			strconv.FormatInt(*k.o.RunAsUser, 10), strconv.FormatInt(*k.o.RunAsGroup, 10), stepUserName, stepHome, sp.Name + "."},
		VolumeMounts:    []corev1.VolumeMount{{Name: userFilesVolume, MountPath: "/launchway-user"}},
		Resources:       k.resources(ContainerSpec{}),
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

// userMounts are the two read-only mounts that put a step container's files over /etc/passwd and /etc/group.
func userMounts(container string) []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: userFilesVolume, MountPath: "/etc/passwd", SubPath: container + ".passwd", ReadOnly: true},
		{Name: userFilesVolume, MountPath: "/etc/group", SubPath: container + ".group", ReadOnly: true},
	}
}

// userInitProblem says why a pod's init containers cannot make the user files, or "" when they have not failed.
func userInitProblem(p *corev1.Pod) string {
	for _, st := range p.Status.InitContainerStatuses {
		if w := st.State.Waiting; w != nil && (w.Reason == "CreateContainerError" || w.Reason == "CreateContainerConfigError") {
			return fmt.Sprintf("the image %s cannot run /bin/sh, which Launchway uses to give the step's user an account entry (%s). "+
				"Steps need /bin/sh in their image; or the runner's operator can set kubernetes.user_entry: false", st.Image, oneLine(w.Message))
		}
		if t := st.State.Terminated; t != nil && t.ExitCode != 0 {
			return fmt.Sprintf("making the step user's account entry in %s failed (exit %d). "+
				"The runner's operator can set kubernetes.user_entry: false to skip it", st.Image, t.ExitCode)
		}
	}
	return ""
}

// stepUserLine is the line a step starts with, so that the log says who the step is.
func (k *Kubernetes) stepUserLine() string {
	if !k.o.restricted() {
		return "Running as the image's own user (kubernetes.restricted: false)"
	}
	if !k.o.userEntry() {
		return fmt.Sprintf("Running as uid %d, gid %d, with no account entry (kubernetes.user_entry: false), HOME=%s", *k.o.RunAsUser, *k.o.RunAsGroup, stepHome)
	}
	return fmt.Sprintf("Running as %s (uid %d, gid %d), HOME=%s", stepUserName, *k.o.RunAsUser, *k.o.RunAsGroup, stepHome)
}
