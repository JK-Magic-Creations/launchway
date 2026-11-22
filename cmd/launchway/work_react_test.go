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

// Time from the command line: a timer, an entry by hand, the report and the CSV.
func TestWorkTimeCommands(t *testing.T) {
	hc := newHostedCLI(t)
	hc.repo("acme/app")
	var out bytes.Buffer
	ada := hc.client("ada", "root", &out)
	mustWork(t, ada, &out, "items", "create", "Ring the bell", "--repo", "acme/app", "--estimate", "2")
	if s := mustWork(t, ada, &out, "time", "running"); !strings.Contains(s, "No timer running") {
		t.Fatalf("running: %q", s)
	}
	if s := mustWork(t, ada, &out, "time", "start", "APP-1"); !strings.Contains(s, "APP-1") {
		t.Fatalf("start: %q", s)
	}
	if _, err := runWork(t, ada, &out, "time", "start", "APP-1"); err == nil {
		t.Error("a second timer was started")
	}
	if s := mustWork(t, ada, &out, "time", "stop", "--note", "done"); !strings.Contains(s, "Logged 0:01 on APP-1") {
		t.Fatalf("stop: %q", s)
	}
	if s := mustWork(t, ada, &out, "time", "log", "APP-1", "90", "--day", "2026-03-01", "--note", "review"); !strings.Contains(s, "Logged 1:30") {
		t.Fatalf("log: %q", s)
	}
	if s := mustWork(t, ada, &out, "time", "show", "APP-1"); !strings.Contains(s, "review") || !strings.Contains(s, "Logged 1:31") || !strings.Contains(s, "points") {
		t.Fatalf("show: %q", s)
	}
	if s := mustWork(t, ada, &out, "time", "report", "--person", "me"); !strings.Contains(s, "ada") || !strings.Contains(s, "Total 1:31") {
		t.Fatalf("report: %q", s)
	}
	if s := mustWork(t, ada, &out, "time", "export"); !strings.HasPrefix(s, "day,person,item,title,minutes,hours,kind,note") || !strings.Contains(s, "APP-1") {
		t.Fatalf("export: %q", s)
	}
	if _, err := runWork(t, ada, &out, "time", "log", "APP-1", "lots"); err == nil {
		t.Error("minutes that are not a number were accepted")
	}
}
