// Package mirror keeps hosted repositories in step with repositories on
// other servers, in both directions.
//
// A pull mirror makes a hosted repository an exact copy of an external
// address: every branch, tag and note, deletions followed, on a schedule and
// on demand. While it is a mirror the repository takes no pushes; "stop
// mirroring" makes it an ordinary repository, explicitly and audited. A push
// mirror copies every accepted update of a hosted repository to an external
// address, asynchronously, with retry and backoff.
//
// Both reach servers on other people's networks, so every address passes the
// egress guard (internal/forge/egress), every credential is a reference the
// server reads when it needs it (never stored, shown or logged), and every
// transfer has a time and a size limit. Import (import.go) uses the same
// pieces to bring many repositories in at once.
package mirror
