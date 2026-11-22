// Package anygit is the provider for a connection of the kind "plain": any Git
// server reached over HTTPS, with no host API at all. Everything it does is Git
// itself: ls-remote to prove a credential and to list refs, a fetch for a
// checkout (which the server's mirror and the Stations do with the connection's
// credential), a push of one annotated tag, and a shallow clone, a commit and a
// push for a file change. The product's own git is run by internal/gitexec (no
// shell, a closed environment), and the credential reaches that one process as
// an HTTP header for the connection's own origin only, with redirects off and
// the addresses pinned to what the egress guard checked.
//
// It is a different thing from internal/forge/providers/plain, which is the
// watcher: that package answers for a Git address a Flight watches with no
// connection and no credential of its own (the server's own settings supply
// whatever git needs). Both belong to the kind "plain"; the watcher's
// connection is the internal one named "plain-git", and the connections this
// package serves are the ones a person adds with a base address and a credential.
//
// A connection. The base address is the server's HTTPS address up to where
// repository paths begin (https://git.example.com, or https://git.example.com/git
// when the repositories live under a prefix). A repository is two path segments
// below it, owner/name, used exactly as given (no ".git" is added or removed:
// write the name the way the server's clone address has it). The owners list,
// when given, names the first segments Flights may use.
//
// SSH is not offered through a connection. The server image has the ssh client
// and a Flight that watches a bare ssh address uses the Git credentials of the
// account the server runs as, but git hands the key to ssh only through options
// of the ssh command line (a file, a host key pin), and the product's runner
// refuses a caller's ssh command line on purpose; no in-process SSH client for
// outside hosts exists. A connection therefore takes https addresses only, and
// says so when given an ssh one.
//
// What a Git server cannot do through Git alone (list repositories, name the
// owner of a credential, hold pull requests, statuses, reviews, rules, releases)
// is declared in Capabilities, each with its reason and the way around it.
package anygit
