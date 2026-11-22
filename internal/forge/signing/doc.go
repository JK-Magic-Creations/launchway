// Package signing is verified commits and tags: people register the SSH keys
// they sign with, and Launchway says, for any commit or annotated tag of a
// hosted repository, whether it was signed by a key it knows.
//
// A signature is read from the object itself (the gpgsig header of a commit,
// the block at the end of a tag), checked against the public key that the
// signature carries, and then judged against the keys people registered:
// verified, signed by an unknown key, a bad signature, unsigned, signed by a
// key that was revoked or had expired when the object was made, or signed in
// a format this build cannot check (OpenPGP and X.509 today). The result also
// says whether the signer is the author or the committer of the object.
// The cryptography is cached per object (an object never changes); who owns
// the key, and whether it was revoked, is judged afresh every time.
//
// The installation has a signing key of its own, kept sealed like the other
// installation secrets, with which Launchway signs the merge commits and
// suggestion commits it makes, so that a rule that requires verified
// commits does not stop the product's own work. Its public half is published
// at /.well-known/launchway/signing-key.pub.
//
// This package knows nothing of repositories on disk: callers give it the raw
// bytes of objects (git cat-file) and receive results.
package signing
