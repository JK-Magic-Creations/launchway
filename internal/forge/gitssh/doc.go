// Package gitssh is the transport of Git over SSH: a small SSH server that
// speaks nothing but the three Git services. There is no shell, no pty, no
// agent or port forwarding, no subsystem; a session may run git-upload-pack,
// git-receive-pack or git-upload-archive on a repository path, and a bare
// "ssh -T" is greeted and sent away.
//
// Like internal/forge/githttp, it does not decide who may do what. It
// settles who is calling (a public key, or a certificate from a trusted
// authority, looked up through a Handler) and what is asked for, and passes
// both to the Handler, which authorises, serves and records through the same
// code the HTTPS transport uses. The package imports no other Launchway
// package: keys, permissions and storage are the Handler's business.
//
// Limits: handshake time, authentication attempts per connection, failed
// attempts per address, connections in all and per address, an idle timeout
// and a ceiling on one connection's duration. Key exchange, ciphers and
// message authentication are modern ones only.
package gitssh
