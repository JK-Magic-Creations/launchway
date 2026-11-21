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

func TestAShorterFunctionIsNotNew(t *testing.T) {
	rec := map[string]int{key("a.go", "funlen", "Function 'f' is too long (211 > 150)"): 1,
		key("a.go", "gocyclo", "cyclomatic complexity 80 of func `f` is high (> 30)"): 1}
	where := map[string][]int{}
	cur := map[string]int{key("a.go", "funlen", "Function 'f' is too long (207 > 150)"): 1,
		key("a.go", "gocyclo", "cyclomatic complexity 79 of func `f` is high (> 30)"): 1}
	if got := newFindings(cur, rec, where); len(got) != 0 {
		t.Fatalf("shorter is not new: %v", got)
	}
	cur = map[string]int{key("a.go", "funlen", "Function 'f' is too long (212 > 150)"): 1,
		key("b.go", "funlen", "Function 'f' is too long (100 > 50)"): 1}
	if got := newFindings(cur, rec, where); len(got) != 2 {
		t.Fatalf("longer, and another file, are new: %v", got)
	}
}
