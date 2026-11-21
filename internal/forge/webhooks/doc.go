// Package webhooks sends the events of hosted repositories to the addresses
// people configure: a hook belongs to one repository or to every repository
// of an owner, lists the events it wants, and carries a secret with which
// each delivery is signed (HMAC-SHA256 in X-Launchway-Signature-256).
//
// Deliveries are rows in the database, so every server sees them and one
// delivers each (a delivery is claimed with a compare-and-set). A failed
// delivery is retried with backoff; a hook whose deliveries keep failing is
// disabled with a plain notice. Every attempt is kept, with the request, the
// response status and the time it took, and can be sent again. Addresses are
// checked by the egress guard when the connection is made: an internal
// address is refused unless the installation allows it.
//
// Nothing here runs a pipeline: a push starts pipelines directly, not
// through a webhook.
package webhooks
