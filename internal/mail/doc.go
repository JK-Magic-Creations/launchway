// Package mail is how Launchway sends e-mail to people: an SMTP sender that
// requires an encrypted connection by default, and a bounded retry queue in
// the database.
//
// The sender connects through the installation's egress guard (internal/egress),
// so the mail server must be a public host or one the administrator listed
// under egress.allow; it starts TLS (STARTTLS, or TLS from the first byte)
// before it authenticates or sends anything, and refuses to continue
// if the server does not offer it, unless the installation says plain
// connections are acceptable. Credentials are references to a secret store,
// read when a message is sent and never stored or logged.
//
// Messages have a plain-text part and a simple HTML part with no images,
// styles, scripts or any other remote content. The queue keeps a message's
// body only until it is sent or has failed for good; every send is audited
// without the body.
package mail
