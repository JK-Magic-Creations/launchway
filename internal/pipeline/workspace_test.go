package pipeline

import (
	"strings"
	"testing"
)

func TestWorkspaceExcludeIsParsedAndChecked(t *testing.T) {
	const steps = "steps:\n  a: { image: x, run: y }\n"
	p, err := Parse([]byte("pipeline: p\nworkspace: { exclude: [web/node_modules, \"build/\"] }\n" + steps))
	if err != nil || p.Workspace == nil || len(p.Workspace.Exclude) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := Parse([]byte("pipeline: p\n" + steps)); err != nil || p.Workspace != nil {
		t.Fatalf("no hint: %+v %v", p.Workspace, err)
	}
	for text, want := range map[string]string{
		"[/abs]":       "is absolute",
		"['!x']":       "starts with !",
		"[..]":         "has a . or .. part",
		"[a/../b]":     "has a . or .. part",
		"['**']":       "whole workspace",
		"['.']":        "whole workspace",
		"['']":         "is empty",
		"[\"a\\\\b\"]": "a character a path may not",
	} {
		_, err := Parse([]byte("pipeline: p\nworkspace: { exclude: " + text + " }\n" + steps))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
	if _, err := Parse([]byte("pipeline: p\nworkspace: nope\n" + steps)); err == nil || !strings.Contains(err.Error(), "expected a mapping") {
		t.Errorf("not a mapping: %v", err)
	}
	if _, err := Parse([]byte("pipeline: p\nworkspace: { keep: [x] }\n" + steps)); err == nil {
		t.Error("an unknown key was accepted")
	}
}
