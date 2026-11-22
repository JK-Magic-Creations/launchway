// Package sdk carries the step kits (steps/go, steps/python, steps/sh and
// js/src/step.ts) so that `launchway step new` can copy them into a new step.
// The files here are the published ones: the kits are tested and released from
// this repository.
package sdk

import "embed"

// Kits holds the kit files by their path below sdk/.
//
//go:embed steps/go/lwstep/lwstep.go steps/python/launchway_step.py steps/sh/launchway-step.sh js/src/step.ts
var Kits embed.FS
