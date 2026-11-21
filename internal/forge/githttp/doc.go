// Package githttp is the transport of Git over smart HTTP: the three
// requests a git client makes (the advertisement, upload-pack, receive-pack),
// the framing around git's stateless-rpc processes, and the limits that make
// a public Git endpoint safe to run: request-size and decompression caps,
// idle and total deadlines, a cap on git processes and on one person's share
// of them, and messages in words.
//
// It does not decide who may do what. The caller (internal/api) has already
// authenticated the request and authorised it for the repository before it
// calls Serve, and nothing here reads the repository storage until then.
// The dumb protocol is not served.
package githttp
