package executor

import (
	corev1 "k8s.io/api/core/v1"
)

// Labels a custom step's pod carries. The chart's network policies select
// on them: pods labelled net=none get no egress at all (not even DNS), pods
// labelled net=hosts get what networkPolicy.steps.customHosts allows.
const (
	labelSandbox = "launchway.dev/step-sandbox"
	labelNet     = "launchway.dev/step-network"
	annoHosts    = "launchway.dev/step-hosts"
)

// sandboxPod gives a custom step's pod what its sandbox asks for and a
// Kubernetes pod can enforce: a read-only root filesystem (the workspace,
// HOME and /tmp are volumes), the network label, no service account token,
// no host aliases. It cannot enforce the list of writable workspace paths
// (the workspace is one volume) nor a list of hosts (a NetworkPolicy has
// addresses, not names); the log says so.
func (k *Kubernetes) sandboxPod(pod *corev1.Pod, sb *Sandbox, sys func(string, ...any)) {
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[labelSandbox] = "custom"
	pod.Labels[labelNet] = sb.Label()
	if len(sb.Network) > 0 {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		hosts := ""
		for i, h := range sb.Network {
			if i > 0 {
				hosts += ","
			}
			hosts += h
		}
		pod.Annotations[annoHosts] = hosts
	}
	pod.Spec.AutomountServiceAccountToken = ptr(false)
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if c.SecurityContext == nil {
			c.SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false),
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
		}
		c.SecurityContext.ReadOnlyRootFilesystem = ptr(true)
	}
	// A pod that is not restricted has no home and tmp volumes; a read-only root needs them.
	have := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		have[v.Name] = true
	}
	for _, v := range []struct{ name, path string }{{"home", stepHomeVolume}, {"tmp", "/tmp"}} {
		if have[v.name] {
			continue
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: v.name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		for i := range pod.Spec.Containers {
			pod.Spec.Containers[i].VolumeMounts = append(pod.Spec.Containers[i].VolumeMounts, corev1.VolumeMount{Name: v.name, MountPath: v.path})
		}
	}
	sys("%s", sb.Describe())
	if len(sb.Network) > 0 {
		sys("The step lists the hosts %v; a Kubernetes pod is limited by addresses, not names, so the pod is labelled %s=%s and the chart's policy for that label (networkPolicy.steps.customHosts) decides what it reaches; the list is recorded on the pod", sb.Network, labelNet, "hosts")
	}
	if len(sb.Write) > 0 {
		sys("The step lists the writable paths %v; the pod's workspace is one writable volume, so the list is recorded but not enforced here (the other executors enforce it)", sb.Write)
	}
}
