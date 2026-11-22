// Package gitlab is the provider for the host kind "gitlab": the public
// service and self-managed installs of it. It speaks only the REST calls named
// in the comments, each cited to its page of the official API documentation
// (https://docs.gitlab.com/api/), with the standard library, to the
// connection's own API host. Every request goes through the egress guard; no
// redirect is followed; the credential is read from its reference at the
// moment of a call and never stored, logged or returned.
//
// Files of a release: the Releases API documents release links
// (https://docs.gitlab.com/api/releases/links/), which point at a URL, and the
// generic package registry
// (https://docs.gitlab.com/user/packages/generic_packages/), which stores a
// file under a project. Launchway uploads each file to the generic package
// registry and links it to the release as a package link; that is the
// documented way to attach a file the host stores itself.
package gitlab
