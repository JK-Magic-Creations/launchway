package storage

import "testing"

// What git 2.47 sends for a push to the branch HEAD points at: a line for HEAD as well.
func TestRealRefUpdatesDropsHEADAndDuplicates(t *testing.T) {
	z := "0000000000000000000000000000000000000000"
	a := "65dc313e29cac57fab6f78645cf5ef3b251c4133"
	in := []RefUpdate{
		{Old: z, New: a, Name: "HEAD"},
		{Old: z, New: a, Name: "refs/heads/main"},
		{Old: z, New: a, Name: "refs/heads/main"},
		{Old: z, New: a, Name: "refs/tags/v1"},
	}
	got := realRefUpdates(in)
	if len(got) != 2 || got[0].Name != "refs/heads/main" || got[1].Name != "refs/tags/v1" {
		t.Fatalf("got %+v", got)
	}
	if len(realRefUpdates(nil)) != 0 {
		t.Fatal("nil in, something out")
	}
}
