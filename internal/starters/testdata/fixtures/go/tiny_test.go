package tiny

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("2+3")
	}
}

func TestAddNegative(t *testing.T) {
	if Add(-1, 1) != 0 {
		t.Fatal("-1+1")
	}
}
