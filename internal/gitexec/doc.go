// Package gitexec is the one place Launchway starts the real `git` program.
//
// Everything that hosts, mirrors, clones, reads or pushes a repository goes
// through [Run], so that one set of rules holds everywhere:
//
//   - git is started with an argument list, never through a shell;
//   - its environment is built from an allow-list: no system or user
//     configuration, an empty HOME, no prompts, no variable that can run a
//     program (external diff, askpass, pager, editor, proxy commands);
//   - settings that can run a program are overridden on the command line, so a
//     repository's own .git/config (a hostile one, for instance) cannot make git
//     run its fsmonitor, ssh command, hooks or credential helper;
//   - only safe transports are allowed for submodules and redirects (not ext::,
//     not file:// from a submodule);
//   - the process runs in its own process group and the whole group is killed
//     when the context ends, a timeout fires, or the output is too large;
//   - a total timeout and an idle timeout (no bytes for a while) apply, and
//     captured output is limited;
//   - an optional per-process memory and CPU ceiling is applied before git is
//     executed (see [Reexec]);
//   - messages that can reach a person are redacted of credentials.
//
// A lint test (TestNoOtherPackageRunsGit) fails the build when production code
// outside this package starts git itself.
//
// Licence note: this package calls the git binary as a separate program. It
// links no Git library, and never go-git.
package gitexec
