package testreport

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// goStream is a `go test -json` stream of n finished tests in one package and then one test that has begun and not
// ended, as a file looks when a size limit cut it in the middle of that test.
func goStream(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"Action":"run","Package":"ex/p","Test":"TestA%d"}`+"\n", i)
		fmt.Fprintf(&b, `{"Action":"pass","Package":"ex/p","Test":"TestA%d","Elapsed":0.01}`+"\n", i)
	}
	b.WriteString(`{"Action":"run","Package":"ex/p","Test":"TestInTheCutOffPart"}` + "\n")
	return []byte(b.String())
}

// A stream that ends in the middle of a test is a crash when the file is whole, and just a start when a limit cut it.
func TestACutGoStreamNeverMarksMissingTestsAsErrored(t *testing.T) {
	data := goStream(3)

	whole, err := Parse(context.Background(), data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if whole.Counts.Errored != 1 || whole.Counts.Total != 4 {
		t.Fatalf("a whole file with a test that never ended: %+v", whole.Counts)
	}

	cut, err := Parse(context.Background(), data, Limits{InputCut: true})
	if err != nil {
		t.Fatal(err)
	}
	if cut.Counts.Errored != 0 || cut.Counts.Failed != 0 || cut.Counts.Passed != 3 || cut.Counts.Total != 3 {
		t.Fatalf("a cut file: %+v", cut.Counts)
	}
	for _, c := range cut.Cases {
		if strings.Contains(c.Message, "did not finish") || c.Name == "TestInTheCutOffPart" {
			t.Errorf("a missing test was reported: %+v", c)
		}
	}
}
