package bootstrap

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func specs() []Spec {
	return []Spec{
		{Name: "lw-root-key", Key: "root.key", Generate: RootKey},
		{Name: "lw-runner-token", Key: "token", Generate: Token(48)},
		{Name: "lw-postgresql", Key: "password", Generate: Token(32)},
		{Name: "lw-setup-code", Key: "code", Generate: SetupCode},
	}
}

func opts(s []Spec, log *slog.Logger) Options {
	return Options{Namespace: "ns", Specs: s, Labels: map[string]string{"app": "launchway"}, Log: log, Backoff: time.Millisecond}
}

func get(t *testing.T, cs *fake.Clientset, name string) *corev1.Secret {
	t.Helper()
	s, err := cs.CoreV1().Secrets("ns").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreatesEverythingOnce(t *testing.T) {
	cs := fake.NewSimpleClientset()
	res, err := Ensure(context.Background(), cs, opts(specs(), nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Outcome != Created {
			t.Errorf("%s: %s, want created", r.Name, r.Outcome)
		}
	}
	root := get(t, cs, "lw-root-key")
	if root.Annotations[AnnotationKeep] != "keep" || root.Labels["app"] != "launchway" {
		t.Errorf("root key metadata: %v %v", root.Annotations, root.Labels)
	}
	// The root key is what the keyring's ParseKey reads: 32 bytes, base64, trailing newline.
	txt := string(root.Data["root.key"])
	if !strings.HasSuffix(txt, "\n") {
		t.Errorf("root key has no trailing newline")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(txt))
	if err != nil || len(raw) != 32 {
		t.Errorf("root key is not 32 bytes of base64: %v len %d", err, len(raw))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{48}$`).Match(get(t, cs, "lw-runner-token").Data["token"]) {
		t.Error("runner token is not 48 letters and digits")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{32}$`).Match(get(t, cs, "lw-postgresql").Data["password"]) {
		t.Error("database password is not 32 letters and digits")
	}
	code := string(get(t, cs, "lw-setup-code").Data["code"])
	if !regexp.MustCompile(`^([A-HJKMNP-Z2-9]{5}-){4}[A-HJKMNP-Z2-9]{5}$`).MatchString(code) {
		t.Errorf("setup code has the wrong shape (%d chars)", len(code))
	}
}

func TestSecondRunChangesNothing(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if _, err := Ensure(context.Background(), cs, opts(specs(), nil)); err != nil {
		t.Fatal(err)
	}
	before := get(t, cs, "lw-root-key").DeepCopy()
	cs.ClearActions()
	res, err := Ensure(context.Background(), cs, opts(specs(), nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Outcome != Present {
			t.Errorf("%s: %s, want already present", r.Name, r.Outcome)
		}
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() != "get" {
			t.Errorf("second run did a %s on %s; only get is allowed", a.GetVerb(), a.GetResource().Resource)
		}
	}
	if string(get(t, cs, "lw-root-key").Data["root.key"]) != string(before.Data["root.key"]) {
		t.Error("the root key changed")
	}
}

func TestExistingSecretIsNeverTouched(t *testing.T) {
	mine := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "lw-root-key", Namespace: "ns"}, Data: map[string][]byte{"root.key": []byte("mine")}}
	cs := fake.NewSimpleClientset(mine)
	if _, err := Ensure(context.Background(), cs, opts(specs()[:1], nil)); err != nil {
		t.Fatal(err)
	}
	if got := get(t, cs, "lw-root-key"); string(got.Data["root.key"]) != "mine" || len(got.Data) != 1 || got.Annotations[AnnotationKeep] != "keep" || got.Annotations[AnnotationPrune] != "Prune=false" || len(got.Annotations) != 2 {
		t.Errorf("an existing Secret: the data must stay and only the two keep annotations be added: %v %v", got.Data, got.Annotations)
	}
	for _, a := range cs.Actions() {
		if v := a.GetVerb(); v != "get" && v != "patch" {
			t.Errorf("verb %s used on an existing Secret", v)
		}
	}
}

func TestExistingSecretWithoutKeyIsAnErrorAndUnchanged(t *testing.T) {
	odd := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "lw-root-key", Namespace: "ns"}, Data: map[string][]byte{"other": []byte("x")}}
	cs := fake.NewSimpleClientset(odd)
	_, err := Ensure(context.Background(), cs, opts(specs()[:1], nil))
	if err == nil || !strings.Contains(err.Error(), "no \"root.key\" key") {
		t.Fatalf("want a clear error, got %v", err)
	}
	if got := get(t, cs, "lw-root-key"); len(got.Data) != 1 || string(got.Data["other"]) != "x" {
		t.Error("the Secret was changed")
	}
}

// Another writer creates the Secret between our get and our create: theirs is kept.
func TestAlreadyExistsRace(t *testing.T) {
	theirs := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "lw-runner-token", Namespace: "ns"}, Data: map[string][]byte{"token": []byte("theirs")}}
	cs := fake.NewSimpleClientset()
	var gets atomic.Int32
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if gets.Add(1) == 1 { // the first look finds nothing; by the second, the other writer has won
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "lw-runner-token")
		}
		return true, theirs, nil
	})
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "lw-runner-token")
	})
	res, err := Ensure(context.Background(), cs, opts(specs()[1:2], nil))
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Outcome != Present {
		t.Errorf("outcome %s, want already present", res[0].Outcome)
	}
}

func TestTransientErrorsAreRetriedForbiddenIsNot(t *testing.T) {
	cs := fake.NewSimpleClientset()
	var n atomic.Int32
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if n.Add(1) < 3 {
			return true, nil, apierrors.NewServiceUnavailable("restarting")
		}
		return false, nil, nil
	})
	if _, err := Ensure(context.Background(), cs, opts(specs()[1:2], nil)); err != nil {
		t.Fatalf("a passing error should be retried: %v", err)
	}
	cs2 := fake.NewSimpleClientset()
	var m atomic.Int32
	cs2.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		m.Add(1)
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "x", errors.New("no"))
	})
	if _, err := Ensure(context.Background(), cs2, opts(specs()[1:2], nil)); err == nil || m.Load() != 1 {
		t.Fatalf("forbidden must fail at once: err %v after %d calls", err, m.Load())
	}
}

func TestNoValueIsEverLogged(t *testing.T) {
	var buf bytes.Buffer
	cs := fake.NewSimpleClientset()
	if _, err := Ensure(context.Background(), cs, opts(specs(), slog.New(slog.NewTextHandler(&buf, nil)))); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	for _, name := range []string{"lw-root-key", "lw-runner-token", "lw-postgresql", "lw-setup-code"} {
		s := get(t, cs, name)
		for _, v := range s.Data {
			if strings.Contains(log, strings.TrimSpace(string(v))) {
				t.Errorf("the log holds the value of %s", name)
			}
		}
	}
	if !strings.Contains(log, "lw-root-key") {
		t.Error("the log should name the Secrets")
	}
}

type nilReader struct{}

func (nilReader) Read([]byte) (int, error) { return 0, errors.New("no randomness") }

func TestRandomnessFailureCreatesNothing(t *testing.T) {
	cs := fake.NewSimpleClientset()
	o := opts(specs()[:1], nil)
	o.Rand = nilReader{}
	if _, err := Ensure(context.Background(), cs, o); err == nil {
		t.Fatal("want an error")
	}
	if l, _ := cs.CoreV1().Secrets("ns").List(context.Background(), metav1.ListOptions{}); len(l.Items) != 0 {
		t.Error("a Secret was made without randomness")
	}
}

func TestBadSpecs(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if _, err := Ensure(context.Background(), cs, Options{Specs: specs()}); err == nil {
		t.Error("no namespace should fail")
	}
	dup := []Spec{specs()[0], specs()[0]}
	if _, err := Ensure(context.Background(), cs, opts(dup, nil)); err == nil {
		t.Error("a duplicate name should fail")
	}
}

func TestSetupCodesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := SetupCode(randReader())
		if err != nil || seen[c] {
			t.Fatalf("code %d: %v (repeat %v)", i, err, seen[c])
		}
		seen[c] = true
	}
}

func randReader() io.Reader { return rand.Reader }
