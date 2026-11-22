package executor

import (
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newFakeAPI is a fake Kubernetes API that refuses a pod as the real one does for the faults checkPod knows: a
// container with no image, a name that is empty, invalid or used twice, a request above its limit. The plain
// fake accepts anything, which is how a run whose verdict step had no image was never seen failing in a test.
func newFakeAPI(objs ...runtime.Object) *fake.Clientset {
	cs := fake.NewSimpleClientset(objs...)
	cs.PrependReactor("create", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
		pod := a.(clienttesting.CreateAction).GetObject().(*corev1.Pod)
		if err := checkPod(pod); err != nil {
			return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonInvalid, Code: 422,
				Message: `Pod "` + pod.Name + `" is invalid: [` + err.Error() + `]`}}
		}
		return false, nil, nil
	})
	return cs
}

func TestCheckPodRefusesWhatTheAPIServerRefuses(t *testing.T) {
	good := corev1.Container{Name: "a", Image: "x"}
	big := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}
	for name, c := range map[string]struct {
		spec corev1.PodSpec
		want string
	}{
		"no image":            {corev1.PodSpec{Containers: []corev1.Container{good, {Name: "b"}}}, "containers[1].image: Required value"},
		"no image in an init": {corev1.PodSpec{InitContainers: []corev1.Container{{Name: "i"}}, Containers: []corev1.Container{good}}, "initContainers[0].image: Required value"},
		"no name":             {corev1.PodSpec{Containers: []corev1.Container{{Image: "x"}}}, "name: Required value"},
		"a name twice":        {corev1.PodSpec{InitContainers: []corev1.Container{{Name: "a", Image: "x"}}, Containers: []corev1.Container{good}}, "Duplicate value"},
		"a bad name":          {corev1.PodSpec{Containers: []corev1.Container{{Name: "Bad_Name", Image: "x"}}}, "Invalid value"},
		"request over limit":  {corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Image: "x", Resources: big}}}, "greater than the limit"},
	} {
		err := checkPod(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Spec: c.spec})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if err := checkPod(&corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{good}}}); err != nil {
		t.Errorf("a good pod was refused: %v", err)
	}
}

// A step with no image is told so by name and runs nowhere; a container nothing in the step needs is left out of
// the pod, so the run's other steps still start.
func TestAContainerWithNoImageIsLeftOutAndItsStepIsRefused(t *testing.T) {
	var said []string
	sys := func(f string, a ...any) { said = append(said, f) }
	specs := []ContainerSpec{{Name: "image-1", Image: "docker.io/library/alpine:3.22"}, {Name: "image-2"}}
	got, err := usableContainers(Spec{Name: "check-code", Containers: specs, Container: "image-1"}, sys)
	if err != nil || len(got) != 1 || got[0].Name != "image-1" || len(said) != 1 {
		t.Fatalf("%+v %v %v", got, err, said)
	}
	_, err = usableContainers(Spec{Name: "inspection", Containers: specs, Container: "image-2"}, sys)
	if err == nil || !strings.Contains(err.Error(), "step inspection has no image") {
		t.Fatalf("the step with no image: %v", err)
	}
}

// The pod of a run that has such a container is still made, with the containers that can be, and the real
// (validating) fake API accepts it.
func TestThePodOfARunWithAnImagelessContainerIsStillMade(t *testing.T) {
	k := testKube(t, KubernetesOptions{Namespace: "steps"})
	k.client = newFakeAPI()
	s := Spec{RunID: "run_1", Name: "check-code", Container: "image-1",
		Containers: []ContainerSpec{{Name: "image-1", Image: "docker.io/library/alpine:3.22"}, {Name: "image-2"}}}
	pod, _, err := k.preparePod(t.Context(), &s, "launchway-run-1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.client.CoreV1().Pods("steps").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("the fake API refused the pod: %v", err)
	}
	for _, c := range pod.Spec.Containers {
		if c.Image == "" {
			t.Errorf("container %s has no image", c.Name)
		}
	}
}
