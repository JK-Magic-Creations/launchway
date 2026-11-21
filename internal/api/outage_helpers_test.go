package api

import (
	"os"
	"path/filepath"
)

func spoolHasResult(root string) bool {
	m, _ := filepath.Glob(filepath.Join(root, "*", "result.json"))
	_, err := os.Stat(root)
	return err == nil && len(m) > 0
}
