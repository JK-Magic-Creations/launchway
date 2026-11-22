package main

import (
	"bytes"
	"strings"
	"testing"
)

// Work for a repository, and reactions, from the command line against a real server.
func TestWorkRepositoryAndReactionCommands(t *testing.T) {
	hc := newHostedCLI(t)
	hc.repo("acme/app")
	hc.repo("acme/lib")
	var out bytes.Buffer
	ada := hc.client("ada", "root", &out)

	if s := mustWork(t, ada, &out, "items", "create", "For the app", "--for-repo", "acme/app"); !strings.Contains(s, "Made ") {
		t.Fatalf("create for a repository: %q", s)
	}
	mustWork(t, ada, &out, "items", "create", "Tied to the app", "--scope", "project:acme/lib", "--for-repo", "acme/app")
	mustWork(t, ada, &out, "items", "create", "Library only", "--scope", "project:acme/lib")
	s := mustWork(t, ada, &out, "items", "list", "--in-repo", "acme/app")
	if !strings.Contains(s, "For the app") || !strings.Contains(s, "Tied to the app") || strings.Contains(s, "Library only") {
		t.Fatalf("a repository's work: %q", s)
	}
	first := ""
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "APP-") {
			first = f
			break
		}
	}
	if first == "" {
		t.Fatalf("no reference in %q", s)
	}
	if s := mustWork(t, ada, &out, "items", "react", first, "heart"); !strings.Contains(s, "heart 1*") {
		t.Fatalf("react: %q", s)
	}
	if s := mustWork(t, ada, &out, "items", "react", first, "heart", "--remove"); strings.Contains(s, "heart") {
		t.Fatalf("react back: %q", s)
	}
	if _, err := runWork(t, ada, &out, "items", "react", first, "banana"); err == nil {
		t.Error("a reaction outside the set was accepted")
	}
}
