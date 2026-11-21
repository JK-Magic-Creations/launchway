// Package forge is the one model of a Git host that Launchway's API, MCP
// tools, command line, Terraform provider and screens talk to, and the one
// Provider interface every backend implements: the plain Git watcher, the
// built-in forge, and each external forge.
//
// Nothing outside this package family may see which backend is behind an
// object. That is enforced by tests, not by convention: no exported type or
// field is named after a provider (TestNoProviderNamesInTheModel), provider
// detail lives only in Ext, which no client may read, and providers are
// reached through a Registry (providers are wired in at start-up).
//
// # Vocabulary
//
// These are Launchway's own words. They are the same in the API, the MCP
// tools, the command line, Terraform and the screens; other forges' words
// appear in documentation only.
//
//	Connection  a configured link to a forge: kind, base URL, a credential
//	            reference, how events arrive. Kind "launchway" is the built-in
//	            forge, "plain" is today's watcher of a Git URL.
//	Repository  a repository on a connection: identity, default branch,
//	            visibility, clone addresses.
//	Home        who owns a repository (an organisation or unit); part of its
//	            path "<home>/<name>" on the built-in forge.
//	Proposal    a request to bring the commits of one ref into another, with a
//	            conversation.
//	Revision    one head of a proposal; a push to its source makes a new one.
//	Review      a person's verdict on a revision: approve, ask for changes,
//	            or comment.
//	Thread      a conversation on a proposal, or on a line of a revision.
//	Result      a named verdict on a commit by a producer: pending, passing,
//	            failing or error, with a link and a one-line reason.
//	Ref rules   rules for refs matching a pattern: who may push, required
//	            reviews and results, allowed ways to land.
//	Land        bring a proposal's commits into the target ref, by a strategy.
//	Site        a static web site published from a repository.
//
// # Proposed (not yet public) shapes
//
// Clone addresses https://<host>/git/<home>/<repo>; screens under
// /code/<home>/<repo>; API under /api/v1/connections, /repositories,
// /proposals. A repository is found by connection and path
// (GET /api/v1/repositories?connection=...&path=...), never by a path in a
// URL, because paths differ between forges and contain slashes. These await
// the owner's confirmation (decision D-FORGE-04).
//
// # Rules that keep the model neutral
//
// No field is named after a provider. Times are UTC. Commit IDs are full.
// Text is Markdown. A feature a backend lacks returns [ErrUnsupported] with a
// plain-words workaround, never an unrelated error.
//
// # Storage independence
//
// The interfaces say nothing about how a built-in repository is stored. The
// storage (internal/forge/storage) is behind its own small interface, so the
// built-in provider works with a repository directory on a volume today and
// with replicated or database-backed storage later.
//
// # Relation to earlier work
//
// No other package named forge exists on main at the time of writing (the
// older, smaller "forge" interface of story S33-11 had not merged). If it
// merges later, its PostStatus becomes Results().Publish, its configuration
// objects import as Connections of kind github, gitea, forgejo or gitlab,
// and its tests stay.
package forge
