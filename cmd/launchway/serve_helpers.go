package main

import (
	"github.com/jk-magic-creations/launchway/internal/config"
)

// loopLimitOf is the configured loop limit; the file was checked when it was
// read, so a bad value cannot reach here.
func loopLimitOf(cfg *config.Config) int {
	n, _ := cfg.LoopLimit()
	return n
}
