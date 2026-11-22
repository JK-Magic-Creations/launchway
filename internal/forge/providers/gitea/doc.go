// Package gitea is the provider for the host kinds "gitea" and "forgejo": one
// implementation, two kinds, because Forgejo keeps the API of the Gitea it
// came from. Both are self-managed by nature (a connection always gives the
// install's address; there is no public service to assume). It speaks only the
// REST calls named in the comments, each cited to the host's own generated
// API reference (the Gitea API reference https://docs.gitea.com/api/ and its
// OpenAPI file https://docs.gitea.com/swagger-24.json, and Forgejo's
// https://codeberg.org/swagger.v1.json for the same operations), with the
// standard library, to the connection's own API host. Every request goes
// through the egress guard; no redirect is followed; the credential is read
// from its reference at the moment of a call and never stored, logged or
// returned.
//
// The two APIs were compared operation by operation for every call used here.
// They differ in details this package handles: Forgejo answers a created tag
// with 201 where Gitea says 200 (both are accepted); a Forgejo branch
// protection has no force-push setting (absent means unknown); webhook
// signatures come in a header named for the host (X-Gitea-Signature or
// X-Forgejo-Signature).
package gitea
