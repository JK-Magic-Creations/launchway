package main

import (
	"reflect"
	"testing"
)

func TestWriterFlag(t *testing.T) {
	var w writerFlag
	if err := w.Set("main=mcp-*"); err == nil {
		t.Fatal("three parts were needed")
	}
	if err := w.Set("main=mcp-*,jkma=apps/mcp-servers/base.yaml"); err != nil {
		t.Fatal(err)
	}
	want := []pipelineRule{{Branch: "main", Projects: []string{"mcp-*", "jkma"}, Files: []string{"apps/mcp-servers/base.yaml"}}}
	if !reflect.DeepEqual(w.rules, want) {
		t.Fatalf("%+v", w.rules)
	}
	if err := w.Set("none"); err != nil || w.rules == nil || len(w.rules) != 0 {
		t.Fatalf("none: %+v %v", w.rules, err)
	}
}
