package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// A step that is measured leaves no error behind from ending its sampling loop or deleting its pod: the client logs
// "Copying stdout failed" when an exec stream is torn down by cancelling it, so the loop is ended from inside. The
// test fails on any such line in what the process printed .
func TestKubernetesStepQuiet(t *testing.T) {
	k := kubeStep(t)
	run := fmt.Sprintf("run_sq%d", time.Now().UnixNano())
	s := kstep(t, t.TempDir(), run, "measured", alpine, "64Mi", "sleep 3; echo done")
	s.Usage = &Usage{}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	var c capture
	code, rerr := k.Run(context.Background(), s, c.sink)
	time.Sleep(2 * time.Second) // late log lines from the streams
	os.Stderr = old
	w.Close()
	out, _ := io.ReadAll(r)
	if rerr != nil || code != 0 {
		t.Fatalf("%d %v\n%s", code, rerr, c.text())
	}
	if strings.Contains(string(out), "Copying std") || strings.Contains(string(out), "closed network connection") {
		t.Fatalf("the client logged errors for a step that succeeded:\n%s", out)
	}
	if !s.Usage.Measured {
		t.Fatalf("the step was not measured: %+v", s.Usage)
	}
}
