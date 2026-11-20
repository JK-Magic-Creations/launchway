package main

import "testing"

func TestOnlyGrowthIsNew(t *testing.T) {
	k := key("a.go", "errcheck", "unchecked")
	where := map[string][]int{k: {3, 9}}
	if got := newFindings(map[string]int{k: 2}, map[string]int{k: 2}, where); len(got) != 0 {
		t.Errorf("same count is not new: %v", got)
	}
	if got := newFindings(map[string]int{k: 1}, map[string]int{k: 2}, where); len(got) != 0 {
		t.Errorf("a fixed finding is not new: %v", got)
	}
	if got := newFindings(map[string]int{k: 3}, map[string]int{k: 2}, where); len(got) != 1 {
		t.Errorf("a third finding must be new, got %v", got)
	}
	if got := newFindings(map[string]int{k: 1}, map[string]int{}, where); len(got) != 1 {
		t.Errorf("an unlisted finding must be new, got %v", got)
	}
}
