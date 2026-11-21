// Package installer is the guided installer behind `launchway install`: a few plain questions,
// a summary of what it is about to do, then the doing, for three kinds of place (this machine on
// its own, a Kubernetes cluster the person already has, and, not yet, a small Kubernetes it sets
// up). It assumes nothing: a missing thing is explained and a way forward offered, never a dead end.
//
// The questions are asked through an Asker, so the same flow runs interactively, from an answers
// file and from flags. Everything that touches the machine goes through an Env (files under a
// root, commands, the network), so the tests run every "nothing present" case with fakes.
// The Kubernetes side lives in internal/installer/kube.
package installer
