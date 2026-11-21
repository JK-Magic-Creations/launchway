// Package githostaccept is the executable form of the Git hosting acceptance
// tests (AT-HG-01 to AT-HG-31 and AT-GAP-9 of the design repository's
// docs/32-replacing-cluster-a-gitea.md, story S40-01).
//
// The suite runs against Launchway's own Git hosting (installation setting
// forge.hosting) and against any other Git host that offers the same
// repository surface: a run against the host being replaced must pass, which
// proves the tests are right. Nothing here knows which one it is talking to,
// except the few checks that are marked for one flavour only
// (LW_ACCEPT_FLAVOR=launchway).
//
// Configuration, all through the environment so no secret is on a command
// line or in a file:
//
//	LW_ACCEPT_URL        base URL of the host, e.g. http://192.168.1.242:3000 (required; unset skips everything)
//	LW_ACCEPT_TOKEN      a token that can create and delete repositories for its owner (required)
//	LW_ACCEPT_URL_ALT    a second address of the same host, e.g. http://git.example.test
//	LW_ACCEPT_FLAVOR     "launchway" turns on the checks that only Launchway must meet; "gitea" (the default) is the host kind the suite was first proven against, with no extra checks
//	LW_ACCEPT_RECEIVER   address of this machine as the host sees it (default: found automatically)
//	LW_ACCEPT_MIRROR_SRC a public repository for the pull-mirror fallback (default: Launchway's GitHub repository)
//	LW_ACCEPT_BIGPUSH_MB size of the large push of AT-HG-02 (default 50; AT-HG-19 asks for 200)
//	LW_ACCEPT_OIDC_PATH  sign-in start path (default /user/oauth2/authentik)
//	LW_ACCEPT_RO_TOKEN   optional read-only token for LW_ACCEPT_RO_REPO (an existing repository): AT-HG-07 then
//	                     proves read works, push is refused, and a second repository is refused
//	LW_ACCEPT_REPORT     a directory: a JSON and a Markdown report are written there
//
// The tests only ever create repositories named lw-acceptance-<random> under the
// token's own user, and delete exactly those at the end. They never touch any
// other repository, setting or user. Git runs with the user's Git
// configuration and credential helpers switched off, and the token travels in
// an environment variable, never in an argument, a URL or the output (every
// line of output is scrubbed of it).
//
// Run:
//
//	LW_ACCEPT_URL=... LW_ACCEPT_TOKEN=... go test -count=1 -timeout 10m -v ./test/acceptance/githost/
package githostaccept
