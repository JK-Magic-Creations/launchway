package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A directory a step makes has the mode the step asked for, as on an ordinary machine: no volume of the pod carries
// the set-group-id bit (which a pod-wide fsGroup would put on it, and every directory made under it would inherit),
// so a test that asserts an exact 0700 passes. Needs the cluster described in kubernetes_test.go.
func TestKubernetesDirectoriesKeepTheirMode(t *testing.T) {
	const script = `mkdir -m 0700 d && stat -c 'workspace=%a' d
mkdir -p sub && mkdir -m 0750 sub/e && stat -c 'nested=%a' sub/e
mkdir -m 0700 /tmp/t && stat -c 'tmp=%a' /tmp/t
mkdir -m 0700 "$HOME/h" && stat -c 'home=%a' "$HOME/h"
(umask 077; mkdir u && stat -c 'umask=%a' u)
stat -c 'root=%a' .`
	for name, k := range map[string]*Kubernetes{"per-step": kubeStep(t), "per-run": kube(t)} {
		t.Run(name, func(t *testing.T) {
			var c capture
			run := fmt.Sprintf("run_mode%d", time.Now().UnixNano())
			s := kstep(t, t.TempDir(), run, "modes", alpine, "64Mi", script)
			code, err := k.Run(context.Background(), s, c.sink)
			if err != nil || code != 0 {
				t.Fatalf("%d %v\n%s", code, err, c.text())
			}
			for _, want := range []string{"workspace=700", "nested=750", "tmp=700", "home=700", "umask=700"} {
				if !strings.Contains(c.text(), want) {
					t.Errorf("no %q in\n%s", want, c.text())
				}
			}
			if strings.Contains(c.text(), "root=2") { // 2xxx: the set-group-id bit on the workspace itself
				t.Errorf("the workspace has the set-group-id bit:\n%s", c.text())
			}
		})
	}
}
