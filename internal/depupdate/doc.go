// Package depupdate is Launchway's own dependency updater. It reads a
// repository's manifests and lock files into one list of dependencies
// (Discover), asks per-ecosystem version sources what is newer and OSV what is
// vulnerable (packages source and osv), decides what to propose under a policy
// with limits (plan), and opens each proposal as a pull request (propose).
//
// An update is only ever a proposal: a branch and a pull request that a person
// reviews and the normal pipeline checks. Lock files are never edited by hand
// on the server; the proposer names the exact toolchain command and the image
// it must run in, and a restricted pipeline run does it (see propose).
//
// Each ecosystem is one small package (gomod, npm, python, container,
// workflow) that implements Parser; adding an ecosystem is adding a package
// and one line in ecosystems. Ecosystems that are designed but not built are
// listed by Coming and never offered as available.
package depupdate
