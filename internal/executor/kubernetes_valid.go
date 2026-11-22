package executor

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// usableContainers is the containers of the run's pod that can be made. A container with no image cannot be a
// container: the API server refuses the whole pod ("spec.containers[13].image: Required value"), and with it every
// step of the run. A step that has no image is told so, by name, and runs nowhere; a container nothing in this step
// needs is left out, and the run's other steps go on. The pipeline's own steps never wait on a step that cannot run.
func usableContainers(s Spec, sys func(string, ...any)) ([]ContainerSpec, error) {
	out := make([]ContainerSpec, 0, len(s.Containers))
	for _, c := range s.Containers {
		if strings.TrimSpace(c.Image) != "" {
			out = append(out, c)
			continue
		}
		if c.Name == s.Container || s.Container == "" && c.Name == s.Name {
			return nil, cannotStart("step %s has no image, so it cannot run in a container; give it an image (or a container) in the pipeline", s.Name)
		}
		sys("Leaving out the container %s: it has no image (no step that runs here needs it)", c.Name)
	}
	return out, nil
}

// checkPod is the API server's basic validation of a pod, so that a pod it would refuse is found before the
// call, with a sentence that names the container, and so that tests with a fake API server meet the same rules:
// every container has a valid, unique name and an image, and no request is above its limit.
func checkPod(pod *corev1.Pod) error {
	seen := map[string]bool{}
	check := func(kind string, i int, c corev1.Container) error {
		at := fmt.Sprintf("spec.%s[%d]", kind, i)
		if c.Name == "" {
			return fmt.Errorf("%s.name: Required value", at)
		}
		if errs := validation.IsDNS1123Label(c.Name); len(errs) > 0 {
			return fmt.Errorf("%s.name: Invalid value %q: %s", at, c.Name, strings.Join(errs, "; "))
		}
		if seen[c.Name] {
			return fmt.Errorf("%s.name: Duplicate value %q", at, c.Name)
		}
		seen[c.Name] = true
		if strings.TrimSpace(c.Image) == "" {
			return fmt.Errorf("%s.image: Required value (container %s)", at, c.Name)
		}
		for r, req := range c.Resources.Requests {
			if req.Sign() < 0 {
				return fmt.Errorf("%s.resources.requests[%s]: Invalid value: must be greater than or equal to 0", at, r)
			}
			if lim, ok := c.Resources.Limits[r]; ok && req.Cmp(lim) > 0 {
				return fmt.Errorf("%s.resources.requests: Invalid value: %s is greater than the limit %s (container %s)", at, req.String(), lim.String(), c.Name)
			}
		}
		return nil
	}
	for i, c := range pod.Spec.InitContainers {
		if err := check("initContainers", i, c); err != nil {
			return err
		}
	}
	for i, c := range pod.Spec.Containers {
		if err := check("containers", i, c); err != nil {
			return err
		}
	}
	if len(pod.Spec.Containers) == 0 {
		return fmt.Errorf("spec.containers: Required value")
	}
	return nil
}

// preparePod makes the pod of a run for its step: only the containers that can be made, sized (see checkResources),
// and checked as the API server would check them. It replaces s.Containers by what the pod has.
func (k *Kubernetes) preparePod(ctx context.Context, s *Spec, name string, sys func(string, ...any)) (*corev1.Pod, map[string]string, error) {
	usable, err := usableContainers(*s, sys)
	if err != nil {
		return nil, nil, err
	}
	specs, err := k.checkResources(ctx, usable, sys)
	if err != nil {
		return nil, nil, err
	}
	s.Containers = specs
	pod, containers := k.podSpec(name, s.RunID, s.Containers)
	if err := checkPod(pod); err != nil {
		return nil, nil, cannotStart("the run's pod would be refused by the API server: %v", err)
	}
	return pod, containers, nil
}
