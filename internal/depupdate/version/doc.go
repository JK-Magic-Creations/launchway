// Package version compares versions the way each ecosystem does: semantic
// versions (Go modules, npm), PEP 440 (Python) and container image tags.
// Pre-releases are recognized so the planner can leave them out by default.
package version
