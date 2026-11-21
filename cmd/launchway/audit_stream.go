package main

import (
	"os"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// startAuditStream writes every audit entry as one JSON line on standard
// output, unless audit.stdout is false (docs/reference/events.md).
func startAuditStream(cfg *config.Config, eng *engine.Engine) {
	if cfg.AuditStdout() {
		eng.SetAuditStream(os.Stdout, "")
	}
}
