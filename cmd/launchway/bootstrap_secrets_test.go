package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBootstrapSecretsCommand(t *testing.T) {
	cs := fake.NewSimpleClientset()
	var log bytes.Buffer
	args := []string{"--namespace", "lw", "--root-key", "r-root-key", "--runner-token", "r-token", "--setup-code", "r-code", "--label", "app=x,tier=y"}
	for i := 0; i < 2; i++ { // twice: the second run is a no-op
		err := runBootstrapSecrets(args, &log, func() (kubernetes.Interface, error) { return cs, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := cs.CoreV1().Secrets("lw").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(l.Items) != 3 {
		t.Fatalf("want 3 Secrets, got %v (%v)", len(l.Items), err)
	}
	for _, s := range l.Items {
		if s.Labels["app"] != "x" || s.Labels["tier"] != "y" {
			t.Errorf("%s: labels %v", s.Name, s.Labels)
		}
		for _, v := range s.Data {
			if strings.Contains(log.String(), strings.TrimSpace(string(v))) {
				t.Errorf("the log holds a value of %s", s.Name)
			}
		}
	}
}

func TestBootstrapSecretsNeedsANamespaceAndEnvWorks(t *testing.T) {
	var log bytes.Buffer
	none := func() (kubernetes.Interface, error) { return fake.NewSimpleClientset(), nil }
	if err := runBootstrapSecrets(nil, &log, none); err == nil {
		t.Error("no namespace should fail")
	}
	t.Setenv("LAUNCHWAY_BOOTSTRAP_NAMESPACE", "lw")
	t.Setenv("LAUNCHWAY_BOOTSTRAP_DB_PASSWORD", "pg")
	cs := fake.NewSimpleClientset()
	if err := runBootstrapSecrets(nil, &log, func() (kubernetes.Interface, error) { return cs, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Secrets("lw").Get(context.Background(), "pg", metav1.GetOptions{}); err != nil {
		t.Error("the Secret named by the environment was not made:", err)
	}
}
