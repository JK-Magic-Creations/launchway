// Package registry is Launchway's built-in OCI registry: container images
// and OCI artifacts (Helm charts, signatures, SBOMs, attestations) stored in
// the product's own blob store, served by the Distribution API under /v2/.
//
// What it keeps, and where:
//
//   - Bytes (layers, configs and manifest bodies) are content-addressed
//     objects in the blob store (internal/blobstore), one per organization and
//     digest: identical content is stored once inside an organization and
//     never shared across two.
//   - Everything else (repositories, which repository may see which blob,
//     manifests and what they need, tags, uploads in progress, policies) is in
//     the database, so that any server of a group may take the next chunk of
//     an upload or answer the next pull.
//
// Nothing here knows who the caller is: the HTTP handler asks an
// Authenticator, and each decision is a Principal's Can. The registry is
// private by default; there is no anonymous pull.
package registry
