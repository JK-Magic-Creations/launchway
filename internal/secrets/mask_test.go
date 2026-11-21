package secrets

import "testing"

func TestMaskerHoldsTheStartOfASecret(t *testing.T) {
	m, _ := NewMasker(map[string]string{"A": "SECRET-VALUE"})
	for in, want := range map[string]int{"plain": 0, "token SECR": 4, "token SECRET-VALU": 11, "SECRET-VALUE": 0, "x S": 1, "": 0} {
		if got := m.Hold(in); got != want {
			t.Errorf("Hold(%q) = %d, want %d", in, got, want)
		}
	}
	var none *Masker
	if none.Hold("SECR") != 0 {
		t.Error("nil masker holds nothing")
	}
}
