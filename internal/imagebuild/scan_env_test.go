package imagebuild

import (
	"os"
	"strings"
	"testing"
)

// The scanner never asks the network whether a newer version exists: not in the step's builder, and not in the
// scripts that scan this product's own image and charts.
func TestScannerSkipsItsVersionCheck(t *testing.T) {
	var got string
	for _, kv := range scannerEnvironment {
		if kv[0] == "TRIVY_SKIP_VERSION_CHECK" {
			got = kv[1]
		}
	}
	if got != "true" {
		t.Errorf("TRIVY_SKIP_VERSION_CHECK is %q in the scanner's environment", got)
	}
	for _, f := range []string{"image-scan.sh", "iac.sh"} {
		b, err := os.ReadFile("../../scripts/security/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "--skip-version-check") {
			t.Errorf("scripts/security/%s runs the scanner without --skip-version-check", f)
		}
	}
}
