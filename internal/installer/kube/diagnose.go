package kube

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Problem is something wrong in the cluster, in plain words, with what to do about it.
type Problem struct {
	Kind string // "image-pull", "unschedulable", "storage", "crash", "config", "other"
	Pod  string
	What string
	Fix  string
}

// Diagnose looks at the pods of the release and translates what keeps them from starting.
func Diagnose(ctx context.Context, cs kubernetes.Interface, ns string) []Problem {
	var out []Problem
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		for _, st := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			if w := st.State.Waiting; w != nil {
				switch w.Reason {
				case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
					out = append(out, Problem{"image-pull", p.Name, fmt.Sprintf("The cluster cannot download the image %s (%s).", st.Image, short(w.Message)),
						"Check the cluster can reach the registry; if it needs a login, create a pull secret and give its name (--set kubernetes.pull_secret=NAME); if it must use a mirror, give the mirror (--set kubernetes.registry=HOST). In a kind cluster, load the image first: kind load docker-image IMAGE."})
				case "CrashLoopBackOff":
					out = append(out, Problem{"crash", p.Name, fmt.Sprintf("%s keeps stopping: %s", st.Name, lastLog(ctx, cs, ns, p.Name, st.Name)),
						"Read the line above; it is the program's own message. The usual causes are a database it cannot reach or a setting it refuses."})
				case "CreateContainerConfigError", "CreateContainerError":
					out = append(out, Problem{"config", p.Name, fmt.Sprintf("%s could not be created: %s", st.Name, short(w.Message)),
						"A Secret or setting it needs is missing. Run the installer again: it makes the secrets the chart needs once."})
				}
			}
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == "Unschedulable" {
				msg := c.Message
				switch {
				case strings.Contains(msg, "unbound immediate PersistentVolumeClaims") || strings.Contains(msg, "persistentvolumeclaim"):
					out = append(out, Problem{"storage", p.Name, "The database wants a disk, and this cluster has nothing to give it (no default storage class, or the class cannot provision).",
						"Name a storage class to use (--set kubernetes.storage_class=NAME), install a storage provisioner, or choose the temporary-storage trial."})
				case strings.Contains(msg, "Insufficient"):
					out = append(out, Problem{"unschedulable", p.Name, "There is not enough free CPU or memory in the cluster to start " + p.Name + ": " + short(msg),
						"Add a node, free capacity, or choose one server instead of two (--set kubernetes.ha=no)."})
				default:
					out = append(out, Problem{"unschedulable", p.Name, "The pod cannot be placed on a node: " + short(msg), "Look at the nodes' taints and labels."})
				}
			}
		}
	}
	return out
}

func short(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 220 {
		s = s[:220] + "..."
	}
	return s
}

func lastLog(ctx context.Context, cs kubernetes.Interface, ns, pod, container string) string {
	tail := int64(3)
	b, err := cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: &tail, Previous: true}).DoRaw(ctx)
	if err != nil || len(b) == 0 {
		return "(no log yet)"
	}
	return short(strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", " | "))
}
