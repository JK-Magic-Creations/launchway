# Keep your code on another host

Hosting repositories in Launchway is optional. A Flight (codebase) can keep its code on a Git host you already use: Launchway holds a **connection** to that host, a narrow service-account token kept as a reference to a secret, and through it your Sorties run on that code, report back to it and write to it. The host is the primary; Launchway never needs a copy it owns.

This page covers the host kinds `github` and `gitlab` (each the public service and self-managed installs of it: give the install's address as the base address) `gitea` and `forgejo` (self-managed installs: the base address is required) `bitbucket` (Bitbucket Cloud) `bitbucket-datacenter` (the self-managed Bitbucket product: a separate API, base address required) `azuredevops` (the cloud service and the self-managed server product) `codecommit` (AWS CodeCommit, signed requests and IAM) and `plain` (any Git server over HTTPS, with no host API). The sections up to "The limits" describe GitHub; [GitLab](#gitlab) [Gitea and Forgejo](#gitea-and-forgejo) [Bitbucket Cloud](#bitbucket-cloud) [Bitbucket Data Center](#bitbucket-data-center) [Azure DevOps](#azure-devops) [AWS CodeCommit](#aws-codecommit) and [Any Git server](#any-git-server) follow, each with its own token scopes, roles or IAM actions. Every kind passes the same conformance suite and the same pages work for all of them.

## What works

| You get | How | Needs |
| --- | --- | --- |
| Choose a repository in the wizard, with search | the connection lists what its token reaches, narrowed to the owners you allow | read |
| A Sortie on every push, tag and pull request | the host's webhook, or polling when it cannot reach Launchway | read, and a webhook |
| Checkout by a Station | the connection's token, handed to the Station for that one Sortie and sent only to the host's address; never printed, never kept in the workspace's Git settings | read |
| A commit status for each Sortie and each failed step | with a link to the Sortie; a switch per Flight | commit statuses write |
| One summary comment on the pull request | updated in place, never repeated; a switch per Flight | pull requests write |
| Cut a release | the annotated tag is made on the host, then the release with its notes and every file the build kept | contents write |
| Launch write-back and version bumps | a file committed to a branch, or a pull request with it (the server side is built; see the limits below) | contents write, pull requests write |
| A refusal before a push to a protected branch | the host's branch protection is read, and a direct write to a branch that requires pull requests is refused with a message that suggests the pull-request mode | administration read (optional) |

Where the host cannot do something the page says so, in words, and offers nothing that cannot work. For this host: Launchway does not create or delete repositories through a connection (create the repository on the host first), does not merge pull requests or submit reviews on the host (do that there), and does not change branch protection (it only reads it). Mark the Launchway status names as required in the host's branch protection when you want a failing Sortie to block the merge there.

## Connect the host

1. On the host, make a service account (or a machine user) and a **fine-grained token** for it, limited to the repositories Launchway should work on. Do not use a person's token.
2. Grant the least that your use needs:

   | To | Grant on the repositories |
   | --- | --- |
   | Read only (choose a repository, run Sorties, checkout) | Metadata: read, Contents: read |
   | Commit statuses | Commit statuses: read and write |
   | The pull request summary comment | Pull requests: read and write |
   | Tags, releases and release files | Contents: read and write |
   | Launch write-back or version bumps by pull request | Contents: read and write, Pull requests: read and write |
   | Refuse a direct write to a protected branch | Administration: read |
   | The webhook (set up by you on the host) | Repository webhooks: read and write for whoever sets it up; the token Launchway holds does not need it |

3. Put the token in a place the server reads: an environment variable of the server, a file on it, or a sealed token. Launchway stores only the name, never the value.
4. Make a secret for the webhook (a long random string) and store it the same way.
5. Connect:

   ```sh
   launchway repos host add acme-gh --kind github \
     --credential-env ACME_GH_TOKEN \
     --webhook-secret-env ACME_GH_HOOK \
     --owner acme
   launchway repos host test acme-gh
   ```

   For a self-managed install add `--base-url https://git.example.com`. An address that is internal is refused unless the server's `forge.mirror_allow` lists it (the one setting an operator edits: it is the same outbound allow-list that push mirrors, imports and the release publisher use, and every call of a connection goes through it); Launchway sends the token only to that address and the host's documented upload address, follows no redirects, and removes the token from every message.

   **Test** says who the token is, when it expires (when the host reports it), and, for each feature above, whether the token has the permission or lacks it and what to grant. The host does not list a token's fine-grained permissions, so a write feature shows as lacking when the token can push to none of the repositories it reaches.
6. Attach a Flight: choose the repository in the wizard, or `launchway repos outside attach FLIGHT --connection acme-gh --repository acme/app`. The two report switches (`--statuses`, `--comment`) are per Flight.

## What the host adds

A connection is an equal choice, not a reduced one: where the host offers more than Launchway's own forge, the Flight's **Code host** page lists it "from" the host, and says whether Launchway can switch it on. For GitHub:

| Addition | What it gives | Launchway drives it |
| --- | --- | --- |
| Auto-merge | the host merges a pull request itself once its required reviews and checks pass | yes: with `write_back.merge: auto` a Launch's pull request has auto-merge switched on right after it is opened (by squash). If the host refuses (auto-merge is off for the repository), the step waits for a person and the Logbook says why |
| Merge queue | approved pull requests are queued and tested together | no: set it up in the host's branch protection |
| Required reviews and code owners | the host decides who must approve | no: the host enforces it; mark Launchway's statuses required beside it |
| Checks on the pull request | each Sortie and failed step shows as a status with a link back | yes: the commit statuses above |

## One home for a Flight

A Flight's repository lives in exactly one place: hosted by this Launchway, or on one connection. Attaching a Flight whose repository is hosted here is refused. A push mirror is the one named way to have both, with the role "alongside" (a copy kept for one stated purpose); an import is "migrating" and ends when it is done. With Git hosting switched off (`forge.hosting` off) Flights on outside repositories work in full (triggers, checkout, statuses, releases and write-back) and the web interface shows no hosted-repository entry.

## Webhooks or polling

Webhooks start a Sortie at once; polling is the fallback and also the check on them. On the host, add a webhook on the repository (or the owner) with:

- the payload address `https://YOUR-LAUNCHWAY/api/v1/outside-hosts/NAME/webhook`
- the content type `application/json`
- the secret from step 4
- the events: pushes, and pull requests (tag pushes arrive as pushes)

Launchway checks the signature with the connection's secret in constant time, refuses a call it cannot verify (an unknown connection, no secret and a wrong signature all answer `404`, and the failed calls count against the caller's address; a connection also accepts at most 300 calls a minute, and the rest answer `429`), ignores a delivery ID it has seen, and records each accepted delivery in the Logbook. A verified event only makes the Flights on that repository poll at once, so a forged or replayed call can at worst cause a poll. If Launchway cannot be reached from the host, do nothing: the Flight is polled on its schedule, as any Git address is.

A pull request from a fork runs under the usual restricted pull-request rule: no secrets, no deploy steps, and the pipeline of the default branch, not the fork's. The audit line says the Sortie came from a fork, and so does the Sortie's page. Launchway asks the host who opened each pull request and whether it is from a fork before it runs one; if the host cannot be asked, the pull request waits (the decision says so) rather than running unchecked.

A Flight whose connection has received no delivery for five minutes is polled through the host's API as well (every minute), so pull requests, their titles and their authors arrive even when the host cannot reach Launchway. The Sortie's page and `launchway runs show` say "Pull request #N: title, by author", with the host's link.

## Hosts without statuses or webhooks

A host that cannot call Launchway, or that has no commit status for a connection to write, is still followed in full:

- **No webhook**: nothing to set up. A Flight whose connection has received no delivery for five minutes is polled through the host's API, as above; a host whose provider has no webhooks at all is only polled.
- **No commit status** (the provider has none, or the host refused one with 403 or 404 because the token may not write it): Launchway posts the result in the pull request's one **summary comment** instead, updated at every change of state, and does not ask the host for a status again for that repository. A commit that is not on a pull request has nowhere to carry the result, so it is shown in Launchway only. The Flight's Code host page says "This host cannot show a status for a Sortie" with the reason and that the comment is used; give the token the permission to write commit statuses to switch back (it is tried again after a restart). The switch "A commit status for each Sortie" is the one that controls this.
- **Neither statuses nor comments**: results are read in Launchway, and the page says so.

## In the web interface

- **Settings > Git hosts** (`/settings/connections`): add a connection (the names of the environment variables that hold the token and the webhook secret, never the values), **test** it (it lists, for each feature, whether the token has or lacks the permission and what to grant), and remove it.
- **Connect a repository** has a picker: choose a connection, search the host, and the address is filled in; the new Flight is attached to that repository when it is created.
- **Code host** (a tab on the Flight, `/projects/NAME/outside`): the attached repository, the two report switches, the webhook address with a copy button and the three setup steps, when a delivery last arrived, and "This host cannot do X: reason, what to do instead" for everything the host lacks.

## How Launchway reads a private repository

The server's own polling and the mirror of a Flight attached to an outside repository use the connection's token: it reaches git as an HTTP header in the one git process's own settings (never in the address, in the mirror's configuration or in a log), only to the connection's own address, with redirects off. The address is checked against the egress allow-list first. A Station gets the token the same way for the checkout.

## The limits

- One token, narrow: an app-installation credential, which would make a short-lived token for each Sortie, is a planned addition; the connection has a place for it and needs no migration to add it. Until then a Station receives the connection's token for the Sortie's checkout.
- The summary comment carries the Sortie's result, step counts, test totals and findings counts: the same numbers the Sortie's page shows.
- A Launch (deploy) writing back to an outside deployment repository: the connection can commit a file and open a pull request, and refuses a direct write to a protected branch, but the deploy step's own push still uses the credential you configured for it. Wiring the step to the connection is planned.
- A connection cannot create repositories, merge, review, or change branch protection.
- Self-managed installs: the API address is the base address plus `/api/v3` and uploads go to `/api/uploads`, as the host documents; give `--base-url` and nothing else unless your install differs.
- This provider is labelled experimental until it has passed the conformance suite against the live service (`LAUNCHWAY_LIVE_GITHUB_TOKEN` and `LAUNCHWAY_LIVE_GITHUB_REPO` run that test).

## GitLab

The kind `gitlab` is the public service or a self-managed install (`--base-url https://git.example.com`; the API is at `<base>/api/v4`, as the documentation states). It does everything the table at the top lists. Where GitLab differs:

- A **project** has a path (`group/subgroup/name`); a Flight's repository is that path. The **owners** list names top-level groups or users, and a project is allowed when its first path element is listed.
- A **merge request** is what GitLab calls a pull request; the pages use its word. GitLab publishes each merge request's head at `refs/merge-requests/N/head`, and Launchway reads them there.
- The webhook is checked by its **secret token** (the `X-Gitlab-Token` header, which GitLab sends as plain text; Launchway compares it in constant time). A replay is dropped by the delivery's unique ID (`webhook-id`, equal to the retry-stable `Idempotency-Key`; a host that sends neither gets an ID made from the body).
- **Fork** means the merge request's source project differs from its target project.
- A commit status has the states pending, success and failed; GitLab has no separate error state, so an errored step is published as failed with "error" first in the description. Descriptions are cut at 255 characters, the documented limit.
- **Release files** are stored in the project's generic package registry (package `launchway-release`, version = the tag) and linked to the release as package links. That is the documented way to attach a file the host stores itself (release links only point at an address). A file whose name is already linked to the release is not uploaded again. GitLab's release API has **no pre-release flag**, so a Launch's "pre-release" mark is not sent; the release is published as a normal release. File names must not contain spaces or start or end with `~` or `@`.
- **Write-back** commits with the repository files API (one call also cuts the new branch) and opens a merge request. A direct write to a branch whose push access level is "No one" is refused before anything is pushed, with a message that names the pull-request mode.
- The summary is **one note** on the merge request, edited in place.
- A token is a personal, project or group access token. Git over HTTPS sends it as the password with the user name `launchway` (the documentation does not name a user name).

What GitLab adds (shown "from GitLab" on the Flight's Code host page):

| Addition | What it gives | Launchway drives it |
| --- | --- | --- |
| Auto-merge | the host merges a merge request itself once its checks pass | yes: `write_back.merge: auto` sets it right after the merge request is opened (`auto_merge`); a host that refuses leaves the step waiting for a person |
| Merge trains | merge requests wait in line and are tested with those ahead | no: set up on the host |
| Approval rules | the host decides who must approve | no: the host enforces it |
| Statuses on the merge request | each Sortie and failed step shows with a link back | yes: the commit statuses |

What GitLab does not let a connection do, and what to do instead: create or delete projects (create on GitLab, then choose it); make or change a draft merge request (its API documentation has no draft parameter: change it on GitLab); merge, review, or change protected branches (do it on GitLab; mark the Launchway status name as required in the project's merge request settings so a failing Sortie blocks the merge); rebase as an auto-merge strategy.

Token scopes and roles, per feature (from the token scope and permission tables in GitLab's documentation; **Test** reports each as has or lacks):

| To | Token scope | Role on the project |
| --- | --- | --- |
| Choose a project, run Sorties, checkout | `read_api` and `read_repository` (or `api`) | Guest or higher |
| Commit statuses | `api` | Developer |
| The merge request note | `api` | Developer (Planner may comment, but statuses need Developer) |
| Tags and release files (package registry) | `api` | Developer |
| Releases | `api` | Maintainer (GitLab's Releases API page says Developer; its permissions page lists "Manage releases" as Maintainer: use Maintainer to be safe) |
| File changes and write-back merge requests | `api` (`write_repository` alone does not work with the API) | Developer |
| Refuse a direct write to a protected branch | `read_api` | the documentation names none; Maintainer manages protected branches |
| The webhook (set by you) | none for the connection | Maintainer or Owner on the project, to add it |

A token expires at midnight UTC on its expiry date; Test shows it for a personal access token (a project or group token has no record of its own to read, and Test says its scopes are unknown). This provider is labelled experimental until it has passed the conformance suite against a live installation (`LAUNCHWAY_LIVE_GITLAB_TOKEN`, `LAUNCHWAY_LIVE_GITLAB_PROJECT` and, for a self-managed install, `LAUNCHWAY_LIVE_GITLAB_BASE` run that test).

```sh
launchway repos host add acme-gl --kind gitlab \
  --credential-env ACME_GL_TOKEN \
  --webhook-secret-env ACME_GL_HOOK \
  --owner acme
launchway repos host test acme-gl
```

## Gitea and Forgejo

The kinds `gitea` and `forgejo` are one implementation under two names: Forgejo keeps the API of the Gitea it came from, and every call used here was compared operation by operation between the two hosts' own OpenAPI files. Both are self-managed by nature, so the base address (`--base-url https://git.example.com`) is required; the API is at `<base>/api/v1`. Where they differ, the provider handles it: a created tag answers 200 on Gitea and 201 on Forgejo, a Forgejo branch protection has no force-push setting (unknown is not treated as immutable), and the webhook signature header is named for the host.

- A repository is `owner/name`. The **owners** list names the host's users or organizations.
- The token is sent as `Authorization: token <token>`. Git over HTTPS uses it as the password with the account's own login as the user name.
- **Choosing a repository** lists what the account owns or contributes to (the host's repository search for the account); a repository you know by name can be attached with `launchway repos outside attach`.
- The webhook is checked by its **signature**: the hex HMAC-SHA256 of the raw body, in `X-Gitea-Signature` (Gitea) or `X-Forgejo-Signature` (Forgejo, which also sends the Gitea-named headers), compared in constant time. A replay is dropped by the delivery ID (`X-Gitea-Delivery` or `X-Forgejo-Delivery`). Events: `push` (branches and tags), `create` (a tag), `pull_request` and `pull_request_sync`. The documentation lists the pull request payload only as an object, so the head, the fork and the title are read from it when present and otherwise asked of the API; an event with no head starts nothing by itself, and Launchway asks the host before a pull request runs.
- A commit status has the states pending, success, failure and error. The summary is **one comment** on the pull request, edited in place (the host's comment API treats a pull request as an issue).
- **Release files** are the release's own attachments (uploaded as the raw file with its name). A pre-release is marked as the host's API allows.
- **Write-back** commits with the contents API (create or update; a new branch is cut in the same call) and opens a pull request. A direct write to a branch whose protection rule disables push, or limits it to a list, is refused before anything is pushed.
- The host has **no call that tells a token's scopes or expiry**, so Test judges the write features by the account's push permission on the repositories it reaches, and says so; a token that is too narrow shows up as a refusal (403) from the call that needs it.

What these hosts add: **merge when checks succeed** (the host schedules the merge of a pull request once its required checks pass; Launchway drives it for a write-back pull request with `write_back.merge: auto`, and cancels it when asked), **branch protection rules** (who may push or merge, required approvals and checks: read by Launchway, set on the host) and **statuses on the pull request**.

What they do not let a connection do: create or delete repositories; make or change a draft pull request (the API has no draft option); merge, review, or change protection (do it on the host; mark the Launchway status names as required in the branch protection so a failing Sortie blocks the merge).

Token scopes and roles (the host's API usage page names the scopes as `read:` or `write:` plus `repository`, `issue`, `user`, and others, with write including read; it does not map scopes to endpoints, so these are the scopes the features need by their names, and a 403 tells you if one is short):

| To | Token scope | Role on the repository |
| --- | --- | --- |
| Choose a repository, run Sorties, checkout | `read:repository` and `read:user` | read |
| Commit statuses | `write:repository` | write |
| The pull request comment | `write:repository` and `write:issue` | write |
| Tags, releases and attachments | `write:repository` | write |
| File changes and write-back pull requests | `write:repository` | write |
| Refuse a direct write to a protected branch | `read:repository` | administrator (the host lists protection for administrators) |
| The webhook (set by you) | none for the connection | administrator, to add it |

```sh
launchway repos host add acme-gt --kind gitea --base-url https://git.example.com \
  --credential-env ACME_GT_TOKEN \
  --webhook-secret-env ACME_GT_HOOK
launchway repos host test acme-gt
```

These providers are labelled experimental until they have passed the conformance suite against a live installation (`LAUNCHWAY_LIVE_GITEA_TOKEN`, `LAUNCHWAY_LIVE_GITEA_BASE`, `LAUNCHWAY_LIVE_GITEA_REPO` and `LAUNCHWAY_LIVE_GITEA_KIND` run that test).

## Bitbucket Cloud

The kind `bitbucket` is **Bitbucket Cloud** (`https://bitbucket.org`, API at `https://api.bitbucket.org/2.0`). **Bitbucket Data Center** (the self-managed product) is a different product with a different API and webhooks: it is the separate kind [`bitbucket-datacenter`](#bitbucket-data-center), and a base address other than `https://bitbucket.org` is refused here with a message that names that kind. A repository is `workspace/name`; the **owners** list names workspaces, and with it Launchway lists those workspaces directly (without it, the account's own workspaces are asked, which needs the `account` scope).

- The token is a **repository, project or workspace access token**, sent as `Authorization: Bearer`. Git over HTTPS uses it as the password with the literal user name `x-token-auth`, as the host documents. An access token has no account: Test names it "access-token" and cannot read its permissions, and says so.
- The webhook is checked by its **signature**: the `X-Hub-Signature` header, `sha256=` and the hex HMAC of the raw body, compared in constant time. The events are `repo:push` (one event for each branch or tag the push changed; a tag is a push of a tag), `pullrequest:created`, `:updated`, `:fulfilled` (merged) and `:rejected` (declined). A replay is dropped by `X-Request-UUID`; the documentation does not say whether a retry reuses it, and if it does not, a retried delivery is only processed again, which makes the Flight poll once more.
- A **build status** is the host's commit status: a status named for the Sortie, updated in place (states in progress, successful, failed and stopped; an errored step is stopped).
- The summary is **one comment** on the pull request, edited in place. A draft pull request can be made and changed. A declined pull request cannot be reopened (the host has no call): open a new one.
- **Write-back** commits with the source API (multipart: the file, a message and the branch; a new branch is made by naming the commit to build on) and opens a pull request. A direct write to a branch with a "push" restriction is refused before anything is pushed, with a message that names the pull-request mode.
- The host publishes no ref for a pull request's head, so a pull request from **another repository (a fork)** has its head commit named by the API but not fetched with the mirror, so a Sortie for it cannot check that commit out. A pull request from a branch of the same repository is fetched with the branches.

**Releases: the host has none.** Bitbucket has tags and a list of downloadable files per repository, but no release object with notes. The capability is declared **none**, with that reason. **Cut a release** therefore does this: the annotated tag is made on Bitbucket; the release page, its notes and its files stay in Launchway, which serves the files; the Cut a release page says "this host has no release object, so the tag is made there and the release page and its files stay in Launchway, which serves the files"; and the Logbook records the publish on the host as skipped, with the reason. (The host's downloads list is not used: it is a different feature, with its own limits, and a download is not a release.)

What Bitbucket adds (shown "from Bitbucket" on the Flight's Code host page): **merge checks** (require passing builds, approvals, resolved comments and no requested changes; set in the repository's branch restrictions; Launchway reads them to refuse a direct write), **default reviewers**, and **builds on the pull request** (the statuses above). Its auto-merge is a setting of the repository ("allow auto merge when builds pass" in the branch restrictions); the API has no call to switch it on for one pull request, so Launchway cannot drive it: with `write_back.merge: auto` the write-back is refused up front with a message that names `person`, and the merge happens on the host.

What a connection cannot do: create or delete repositories, merge, review or change branch restrictions (do it on Bitbucket).

Scopes per feature (the OAuth scope each operation lists in the host's own OpenAPI file; a repository access token needs the same permissions):

| To | Scope |
| --- | --- |
| Choose a repository, run Sorties, checkout, refs and commits | `repository` |
| List the account's workspaces and its permissions on repositories (Test) | `account` |
| Read pull requests, and post or edit the summary comment | `pullrequest` |
| Build statuses | `repository` |
| Tags | `repository:write` |
| File changes (write-back) | `repository:write` |
| A write-back pull request, update or decline one | `pullrequest:write` |
| Refuse a direct write to a protected branch (branch restrictions) | `repository:admin` |
| The webhook (set by you) | `webhook`, for whoever sets it up; the token Launchway holds does not need it |

The host reports no token scopes or expiry through its API, so Test judges the write features by the account's write permission where the token can read it, and otherwise says that a refusal (403) will show when the feature is used.

```sh
launchway repos host add acme-bb --kind bitbucket \
  --credential-env ACME_BB_TOKEN \
  --webhook-secret-env ACME_BB_HOOK \
  --owner acme
launchway repos host test acme-bb
```

This provider is labelled experimental until it has passed the conformance suite against the live service (`LAUNCHWAY_LIVE_BITBUCKET_TOKEN` and `LAUNCHWAY_LIVE_BITBUCKET_REPO` run that test).

## Bitbucket Data Center

The kind `bitbucket-datacenter` is the self-managed Bitbucket product (Data Center, formerly Server). It is **not** the kind `bitbucket`: the API is different (REST under `<base>/rest/api/latest`, `/rest/build-status/latest`, `/rest/branch-permissions/latest` and `/rest/git/latest`), the webhooks are different, and the address of Bitbucket Cloud is refused here with a message that names the kind `bitbucket`. The **base address is required** and keeps its context path if the install has one (`https://git.example.com` or `https://example.com/stash`). A repository is `PROJECTKEY/name`; a personal repository has the project key `~USER` (for example `~EVE/notes`). The **owners** list names project keys (case does not matter); with it Launchway searches those projects only (the search resource's `projectkey` filter, available since Bitbucket 8.0), without it the repositories the token can read.

- The token is an **HTTP access token**, sent as `Authorization: Bearer` for the REST API. Git over HTTPS uses a **project or repository** token as the password with the literal user name `x-token-auth`, as the host documents; a personal token's Git login is its owner's own user name, which a connection does not know, so use a project or repository token for the connection. The host's page is inconsistent about Basic authentication with these tokens (its introduction warns against a user name, its usage section gives `x-token-auth`); that is a point to check on a real install.
- The token's **permissions** are repository and project levels: read, write, admin. A repository token cannot hold project permissions; the repository level must be at least the project level. Test names the account "access-token": the host's reference has no call that names a token, its kind, its scopes or its expiry, so the connection test lists what the token can do (the repositories it holds read, write and admin permission on, through the search resource's `permission` filter) and cannot report anything else. It cannot see the webhook.
- The webhook is checked by its **signature**: the `X-Hub-Signature` header, `sha256=` and the hex HMAC of the raw body (the host's default algorithm), compared in constant time. The events are `repo:refs_changed` (one event for each branch or tag the push changed; a tag is a push of a tag), `pr:opened`, `pr:from_ref_updated`, `pr:modified` (a changed title, description, draft state or target), `pr:merged`, `pr:declined` and `pr:deleted`. A replay is dropped by `X-Request-Id`; the documentation does not say whether a retry reuses it, and if it does not, a retried delivery is only processed again, which makes the Flight poll once more. A pull request is from **another repository (a fork)** when its `fromRef` repository is not its `toRef` repository.
- A **build status** is the host's build status on the commit: one named for the Sortie, replaced by its key (states in progress, successful, failed and cancelled; an errored step is cancelled). The host requires an address on every status: the Sortie's, or the install's own when the result has none.
- The summary is **one comment** on the pull request, edited in place (the edit reads the comment's current version first). A draft pull request can be made and changed. A declined pull request can be reopened.
- **Write-back** commits with the host's file edit (multipart: the file, a message, the branch, and the branch to start a new one from; an existing file is edited against the commit that last changed it, and a file that changed since is refused by the host) and opens a pull request. A direct write to a branch with a pull-request-only or read-only branch permission is refused before anything is pushed, with a message that names the pull-request mode.
- The host publishes no ref for a pull request's head in its reference, so a pull request from **another repository (a fork)** has its head commit named by the API but not fetched with the mirror, so a Sortie for it cannot check that commit out. A pull request from a branch of the same repository is fetched with the branches.

**Auto-merge: the host has it, and Launchway can ask for it.** With `write_back.merge: auto` Launchway asks the host to merge the write-back pull request itself once every merge check passes (the merge call with `autoMerge`, with the merge strategy `no-ff` or `squash`), and can cancel the request. The repository's or project's settings must allow auto-merge; if they do not, the request is refused up front with a message that says where to turn it on. Only merge and squash are asked for: the reference's pages name no other strategy ids here. Merging at once, reviewing and changing merge checks stay on the host.

**Releases: the host has none.** Bitbucket has tags but no release object with notes or files. The capability is declared **none**, with that reason, exactly as for Bitbucket Cloud. **Cut a release** makes the annotated tag on Bitbucket (the tag call, type annotated); the release page, its notes and its files stay in Launchway, which serves the files; the page says so, and the Logbook records the publish on the host as skipped, with the reason.

What Bitbucket Data Center adds (shown "from Bitbucket Data Center" on the Flight's Code host page): its own **auto-merge** (above), **merge checks** (required approvals, successful builds and completed tasks; set on the repository; Launchway reads the approval and build counts and the branch permissions to refuse a direct write), **default reviewers**, and **builds on the pull request** (the statuses above).

What a connection cannot do: create or delete repositories, merge at once, review, or change branch permissions and merge checks (do it on Bitbucket).

Permissions per feature (project and repository levels of an HTTP access token; the permission each operation lists in the product's own OpenAPI file):

| To | Permission |
| --- | --- |
| Choose a repository, run Sorties, checkout, refs, commits, pull requests (read) | project read and repository read |
| Build statuses | repository read (the call needs only that) |
| Post and edit the summary comment, open a pull request | repository read (a pull request is edited by its author, which is the token's user) |
| Tags | repository write |
| File changes (write-back) | repository write |
| Update, decline or reopen a write-back pull request you did not open | repository write |
| Auto-merge of a write-back pull request | repository write, and auto-merge enabled in settings |
| Refuse a direct write to a protected branch (branch permissions) | repository admin (the host lists them only to repository administrators) |
| Merge checks (approval and build counts) | repository read |
| The webhook (set by you) | repository admin (or project admin for a project-level webhook), for whoever sets it up; the token Launchway holds does not need it |

The host reports no token scopes or expiry through its API, so Test judges the features by the permissions it can read on the repositories, and says that a refusal (403) will show when a feature is used.

```sh
launchway repos host add acme-dc --kind bitbucket-datacenter \
  --base-url https://git.example.com \
  --credential-env ACME_DC_TOKEN \
  --webhook-secret-env ACME_DC_HOOK \
  --owner ACME
launchway repos host test acme-dc
```

This provider is labelled experimental until it has passed the conformance suite against a live install (`LAUNCHWAY_LIVE_BITBUCKETDC_TOKEN`, `LAUNCHWAY_LIVE_BITBUCKETDC_BASE` and `LAUNCHWAY_LIVE_BITBUCKETDC_REPO` run that test). The calls are taken from the product's OpenAPI file for version 10.2 (the reference pages themselves are scripts and cannot be read as text); the behavior of older versions was not checked.

## Azure DevOps

The kind `azuredevops` is the **cloud service** (`https://dev.azure.com`; leave the base address empty) or the **self-managed server product** (give its address with the collection: `--base-url https://devops.example.com/tfs/DefaultCollection`; the API is at `<base>/{project}/_apis/...` and the reference page for the server product's version 7.1 lists the same calls). Every call names `api-version=7.1`.

- A repository is `organization/project/repository` (on the server product the first element is the collection's name). The **owners** list names organizations (on the cloud service it is **required**: the calls Launchway uses name no organization of a token, so the connection lists the ones you allow; on the server product the collection is the base address's and the list may be empty). An owner entry cannot narrow to one project: limit the token's owner to the projects Launchway may reach with the host's own project permissions.
- The token is a **personal access token**, sent as the password of HTTP Basic with an empty user name, as the host documents. Git over HTTPS uses the token as the password with the user name `launchway` (the host says the user name can be empty, so any name is accepted).
- **Test** reads the projects and repositories of the allowed organizations and one branch policy. The REST reference has **no call that names a token's owner, lists its scopes or tells its expiry** (the profile and token-management calls are on a separate host, are previews, and need other scopes), so Test names the account "personal-access-token", and the four write features are listed with what each needs and marked "not checked": Launchway does not write to test. A token that is too narrow shows up as a refusal (401 or 403) from the call that needs it, with the host's message.
- **Webhooks are weaker here.** A service hook carries **no signature of the body**. The Web Hooks consumer can send a basic-authentication user name and password and extra HTTP headers, and Launchway accepts the connection's webhook secret in either: as the **password** of basic authentication (any user name) or as the header `X-Launchway-Webhook-Secret`. The secret is compared in constant time; a delivery with neither, or a wrong one, is refused. The secret travels with every delivery and covers nothing of the payload, so the hook must use **HTTPS**, and nothing in a payload is trusted without asking the host. A replay is dropped by the notification's own `id` in the payload (the host sends no delivery header); a payload without one is named by its notification number, else by a hash of the body. Events: `git.push` (one event for each ref updated, a tag included), `git.pullrequest.created`, `git.pullrequest.updated` (a completed status is a landed pull request, an abandoned one a closed pull request) and `git.pullrequest.merged` (a merge attempt: treated as a revision). Polling stays on as the fallback.
- A **commit status** is posted with context genre `launchway` and the Sortie's name, and the same context posted again is updated in place; when the token can also read pull requests, the status is posted on each active pull request whose head is that commit too (a pull request status), which is what a **status check policy** reads. The policy names the status `genre/name`: for the status `launchway/ci` it is `launchway/launchway/ci`.
- The summary is **one comment thread** on the pull request: the comment is edited in place, and posted again if it was deleted. A draft pull request can be made; the host's reference does not list the draft flag among what an update may change, so Launchway does not change it (make it ready on the host). An abandoned pull request can be reopened.
- **Fork** means the pull request carries `forkSource`. The host publishes no ref for a pull request's head that the reference names, so a pull request from a fork has its head named by the API but not fetched with the mirror.
- **Write-back** commits with the pushes call (one call also cuts a new branch from the commit named as the old object id; add or edit is decided by asking whether the file is there) and opens a pull request. A direct write to a branch with a required (blocking) branch policy is refused before anything is pushed, with a message that names the pull-request mode; the API does not say whether the token's owner may bypass policies, so a user who can is refused anyway (use the pull-request mode).
- **Tags** are annotated tags (the annotated tags call). The reference does not say that call also creates the tag's ref, so Launchway reads it back and, if it is missing, creates it.

**Releases: the host has none.** Azure DevOps Git has tags and no release object. The capability is declared **none**, exactly as for Bitbucket: **Cut a release** makes the annotated tag on the host, and the release page, its notes and its files stay in Launchway, which serves the files; the page says so and the Logbook records the publish on the host as skipped, with the reason.

What Azure DevOps adds (shown "from Azure DevOps" on the Flight's Code host page):

| Addition | What it gives | Launchway drives it |
| --- | --- | --- |
| Auto-complete | the host completes a pull request itself once every required policy passes | yes: `write_back.merge: auto` sets it right after the pull request is opened (squash by default, with the pull request's author as the identity that sets it); the host refuses if the branch has nothing to wait for. **Cancelling** it is on the host: the reference names no call for it |
| Branch policies | minimum approvers, named reviewers, a passing build, a status check | no: set on the host; Launchway reads them to refuse a direct write |
| Required reviewers | reviewers added by path or rule, blocking completion | no: set up on the host |
| Build validation | a build for each pull request that must pass | no: set on the host |
| Statuses on the pull request | each Sortie and failed step with a link back | yes: the statuses above |

What a connection cannot do: create or delete repositories, merge or complete a pull request itself (the host's auto-complete does it), vote or review, change branch policies, or change a draft flag (do it on Azure DevOps).

Scopes and roles per feature (the scopes are the ones each reference page lists; roles are the host's default Git security groups: **Readers** may read and vote, **Contributors** may also contribute, create branches and create tags):

| To | PAT scope | Role on the project |
| --- | --- | --- |
| Test, choose a repository | Project and team (read) `vso.project`, Code (read) `vso.code` | Readers |
| Checkout, polling, refs, commits, pull requests read, branch policies read | Code (read) `vso.code` | Readers |
| Commit and pull request statuses | Code (status) `vso.code_status`, or Code (read and write) `vso.code_write` | Contributors |
| The pull request summary comment | Pull request threads (read and write) `vso.threads_full`, or `vso.code_write` | Contributors |
| File changes and write-back pull requests, auto-complete, reopening and closing | Code (read and write) `vso.code_write` | Contributors (Contribute, Create branch) |
| Tags | Code (full) `vso.code_manage` (the annotated tag call lists no smaller scope) | Contributors (Create tag) |
| The service hook (set by you) | none for the connection | Project Administrators or Project Collection Administrators, to add it |

Set up the webhook on the host: **Project settings, Service hooks, Create subscription, Web Hooks**; trigger **Code pushed**, **Pull request created**, **Pull request updated** and **Pull request merge attempted**, each limited to the repository; on the action give the address `https://YOUR-LAUNCHWAY/api/v1/outside-hosts/NAME/webhook` (HTTPS only; the host's resource details can be left at All), **Basic authentication password** = the connection's webhook secret (any user name, for example `launchway`) **or** the **HTTP headers** line `X-Launchway-Webhook-Secret:<the secret>`, then **Test**.

```sh
launchway repos host add acme-ado --kind azuredevops \
  --credential-env ACME_ADO_TOKEN \
  --webhook-secret-env ACME_ADO_HOOK \
  --owner acme
launchway repos host test acme-ado
launchway repos outside attach shop --connection acme-ado --repository acme/web/shop
```

This provider is labelled experimental until it has passed the conformance suite against a live service (`LAUNCHWAY_LIVE_AZUREDEVOPS_TOKEN`, `LAUNCHWAY_LIVE_AZUREDEVOPS_REPO` as `organization/project/repository` and, for the server product, `LAUNCHWAY_LIVE_AZUREDEVOPS_BASE` run that test).

## AWS CodeCommit

The kind `codecommit` is the cloud provider's Git service. It differs from every other kind in three ways: requests are **signed** with an access key (there is no token), the host has **no webhook, no commit status, no release object and no tag call**, and who may do what is decided by **IAM policy**, not by scopes. Each of those is declared in the capabilities and shown on the Flight's Code host page, with the way around it.

**The base address is the Git endpoint of the repositories' region**, which carries the region: `--base-url https://git-codecommit.us-east-2.amazonaws.com` (also `git-codecommit-fips.<region>.amazonaws.com`, and the `.amazonaws.com.cn` domain). It is required. The API endpoint `codecommit.<region>.amazonaws.com` and, for a role, the token service `sts.<region>.amazonaws.com` are derived from it, so a connection is for one region; make one connection for each region. A repository is `account/name`: the 12-digit account id and the repository name (`123456789012/web`). The **owners** list names account ids and may be empty.

### The credential

The credential reference names one secret holding a small JSON document (Launchway never stores it; it is read when a call is made):

```json
{"access_key_id": "AKIA...", "secret_access_key": "..."}
```

Optional fields: `session_token` (when the key pair is itself temporary), and, to have Launchway **assume a role** with the key pair, `role_arn`, `external_id`, `role_session_name` (default `launchway`) and `duration_seconds` (900 to 43200, 3600 by default). With a role, Launchway calls the token service's `AssumeRole` (a signed request, no AWS SDK), keeps the temporary credentials **in memory only**, renews them five minutes before they expire, and never logs or returns them. Prefer a role: the key pair then only needs permission to assume it, and what Launchway can do is the role's policy. A document with a field it does not know, or an invalid value, is refused with a message that does not repeat the credential.

```sh
launchway repos host add acme-aws --kind codecommit \
  --base-url https://git-codecommit.us-east-2.amazonaws.com \
  --credential-env ACME_AWS_CREDENTIAL \
  --owner 123456789012
launchway repos host test acme-aws
launchway repos outside attach shop --connection acme-aws --repository 123456789012/web
```

**Git is signed per request too.** The server image has no shell and no credential helper, so Launchway does in process what the AWS CLI's credential helper does: the user name is the access key id (followed by `%` and the session token for temporary credentials) and the password is the time, a `Z`, and the signature of the request `GIT` for the repository's path (`/v1/repos/<name>`) and host. It depends on the path and the time, so it is made for each clone, fetch or push and is good for a few minutes. The host's static **Git credentials** (the service-specific user name and password) are not used: the host says they cannot be used with rotating or temporary credentials, and a role is the better practice.

### The IAM actions each feature needs

Give the role (or user) these on the repositories (`arn:aws:codecommit:<region>:<account>:<repository>`), least first. Actions marked `*` take the resource `*` in the host's permission reference.

| Feature | IAM actions |
| --- | --- |
| Choose a repository | `codecommit:ListRepositories` (`*`), `codecommit:BatchGetRepositories` |
| Run Sorties, checkout | `codecommit:GitPull`, `codecommit:ListBranches`, `codecommit:GetBranch`, `codecommit:GetCommit` |
| Follow pull requests (polling), read approvals | `codecommit:ListPullRequests`, `codecommit:GetPullRequest`, `codecommit:GetPullRequestApprovalStates`, `codecommit:ListAssociatedApprovalRuleTemplatesForRepository` (`*`), `codecommit:GetApprovalRuleTemplate` (`*`) |
| Summary comment (where results go) | `codecommit:PostCommentForPullRequest`, `codecommit:UpdateComment` (only the comment's author may edit it) |
| Tags | `codecommit:GitPull`, `codecommit:GitPush` |
| File changes and write-back pull requests | `codecommit:GetBranch`, `codecommit:CreateBranch`, `codecommit:PutFile`, `codecommit:CreatePullRequest` |
| Close or edit a pull request | `codecommit:UpdatePullRequestStatus`, `codecommit:UpdatePullRequestTitle`, `codecommit:UpdatePullRequestDescription` |
| Assume the role (on the key pair's user) | `sts:AssumeRole` on the role, and the role's trust policy must trust that user (and require your `external_id`, if you set one); the role's maximum session duration must be at least `duration_seconds` |

The host also requires that no policy on the identity denies the AWS Key Management Service actions it uses (`kms:Encrypt`, `kms:Decrypt`, `kms:ReEncrypt`, `kms:GenerateDataKey`, `kms:GenerateDataKeyWithoutPlaintext`, `kms:DescribeKey`). **Test** reads and lists the permissions it did not try, marked "not checked"; a missing one shows as a 403 (`AccessDeniedException`) from the call that needs it, with the host's message.

### What Launchway does with it

- **Events are polled.** CodeCommit has no webhook to an address you choose: its events leave through the cloud provider's own event and notification services. The capability is declared with that reason, and the connection is followed by polling (the Flight's repository and its open pull requests): a new pull request and a moved source branch are found; a delivery posted to the connection's webhook address is refused. To react faster, route the host's events to a service of yours that calls Launchway's API.
- **Results go to the pull request's one summary comment.** There is **no commit status** (declared none), so each Sortie's result is written into the pull request's one comment, which Launchway edits in place and posts again if it was deleted or if the credentials changed (only a comment's author may edit it). A commit with **no pull request has nowhere to say it**: nothing is sent and the Flight's page says so; read the result in Launchway. A comment is cut at the host's 10,240 characters.
- **Tags are made with Git**, because the API has no call that creates one. Launchway makes the annotated tag with the product's own git and pushes that one tag, signed as above; it needs `GitPull` and `GitPush`. A tag already at that commit is accepted; one that points elsewhere is refused. It fetches the repository's branches into a scratch directory to have the commit, so a very large repository takes a while the first time.
- **Releases: the host has none.** The capability is declared **none** as for Bitbucket and Azure DevOps: **Cut a release** makes the tag, and the release page and its files stay in Launchway.
- **Write-back** writes one file with `PutFile` (a file is 1 byte to 6 MB; a new branch is cut with `CreateBranch` from the base branch's head; the same content again is not an error) and opens a pull request with `CreatePullRequest`. It cannot merge: **merge, auto-merge and reopening** are not done through a connection. A closed CodeCommit pull request cannot be reopened (a status goes only from open to closed): open a new one. There are **no drafts**.
- **Forks do not exist**: a pull request names one repository for its source and destination, so every pull request comes from a branch of its repository and fork detection has nothing to detect. The host publishes no ref for a pull request's head; Launchway uses the source branch's commit.
- **Approval rules** are read, never changed: the approval rule templates associated with the repository (their destination branches and the number of approvals needed) and each pull request's approval states (who approved or revoked). A template governs the **merge** of a pull request; it does not stop a push, which only IAM policy limits and Launchway cannot read, so a direct write is not refused here.
- **Identity.** The host has no call Launchway uses that names the owner of an access key, so Test shows the key (masked) or the assumed role session.

What a connection cannot do: create or delete repositories, merge or approve a pull request, change an approval rule, or write a commit status (the host has none).

What CodeCommit adds (shown "from AWS CodeCommit" on the Flight's Code host page):

| Addition | What it gives | Launchway drives it |
| --- | --- | --- |
| Approval rules | a pull request needs a number of approvals from named people or roles before it may be merged | no: set on the host through templates; Launchway reads them |
| Approval rule templates | the same rule created on every new pull request of the repositories it is associated with, for chosen destination branches | no: set up on the host |
| Approval state | who approved or revoked their approval of the current revision of a pull request | no: read and shown on the Flight |
| IAM access control | who may read, push, tag or merge is decided by IAM policy (and the role Launchway assumes) | no: set in IAM |

This provider is labelled experimental until it has passed the conformance suite against a live service. The opt-in test is read-only: `LAUNCHWAY_LIVE_CODECOMMIT_CREDENTIAL` (the credential JSON), `LAUNCHWAY_LIVE_CODECOMMIT_BASE` (the region's Git endpoint) and `LAUNCHWAY_LIVE_CODECOMMIT_REPO` (`account/name`) run it. The strict fake recomputes every signature (the API's, the token service's and the Git password's) and serves a real repository over Git, so the signing and the tag push are proved in the unit tests; only the host's own answers cannot be.

## Any Git server

The kind `plain` is a connection to **any Git server that speaks Git over HTTPS**, with no host API at all: a self-hosted bare server, a small Git service with no REST interface Launchway has a provider for, or a host Launchway does not know. Everything goes through Git itself, run by the product's own git (no shell, only a short list of variables). This is the connection kind; a Flight that watches a bare Git address with no connection (the server's own Git settings supply the credential, SSH included) keeps working exactly as before and is not changed by it.

**The base address is the server's HTTPS address up to where repository paths begin**: `--base-url https://git.example.com`, or with a prefix `https://git.example.com/git` when the clone addresses are `https://git.example.com/git/team/app.git`. It is required. `http` is accepted only for a loopback address; a credential is never sent over plain http. **SSH is not offered through a connection**: git hands a key to ssh only through the ssh command line, which the product's git wrapper refuses to take from a caller on purpose, and Launchway has no SSH client of its own for outside hosts. Give the server's https address; for a server that offers only SSH, keep using a Flight that watches the bare ssh address (it uses the Git credentials of the account the server runs as) and loses what a connection adds (tags and write-back through the connection, the credential kept by reference).

**A repository is two names, `owner/name`, exactly as the server's clone address ends** (`team/app.git` for the address above; Launchway adds or removes nothing, so write `.git` if the server's address has it). A Git server cannot list its repositories, so there is **no repository search**: enter the path. Launchway proves it, and the credential, with `git ls-remote` when you attach the Flight, and reads the default branch from it. The **owners** list, when given, names the first path names Flights may use.

### The credential

The credential reference names one secret, read when a call is made and never stored:

- **A token**: the secret is one line, sent as the HTTP Basic password with the user name `git`. Use this for a server that takes an access token as the password (most do, and most ignore the user name).
- **A user name and a password**, or a token with the user name the server wants: a small JSON document, `{"username": "mary", "password": "..."}` or `{"username": "mary", "token": "..."}` (`username` defaults to `git`; a password and a token are not both given). A document with a field it does not know, or a bad value, is refused with a message that does not repeat the credential.

```sh
launchway repos host add acme-git --kind plain \
  --base-url https://git.example.com \
  --credential-env ACME_GIT_TOKEN \
  --owner team
launchway repos host test acme-git
launchway repos outside attach shop --connection acme-git --repository team/app.git
```

The credential reaches git as an HTTP header in that one process's own settings, for the connection's origin only, with redirects off; it is never in an address, a mirror's configuration or a log, and is removed from every message. The base address is checked against the egress allow-list first (an internal address needs `forge.mirror_allow`), and git is pinned to the addresses that check found.

### What the server's account needs

| Feature | The account on the server needs |
| --- | --- |
| Choose a repository (`ls-remote`), run Sorties, checkout, polling | read (clone and fetch) on the repository |
| Tags (**Cut a release**) | write (push) of tags on the repository; a server that restricts tag creation by rule must allow this account |
| File changes and write-back, directly or to a new branch | write (push) to the branch, or to the new branch, on the repository, and no protection rule that stops this account pushing there |

**Test** reads only the credential: a Git server names no account and lists no repository, so Test shows the user name, checks the form of the credential, and lists the write features as "not checked" (Launchway writes nothing to test). A missing permission shows when the feature is first used, as the server's own message.

### What Launchway does with it

- **Events are polled.** A Git server sends no webhook Launchway can check, so the repository itself is fetched on the server's polling schedule and a push or a new tag is found there; a delivery posted to the connection's webhook address is refused. **To react at once**, call the Flight's git-hook address from the server's `post-receive` hook. Give the Flight `git_webhook: { kind: gitlab, secret_env: ... }` (the kind only names how the call proves itself: the secret goes in the `X-Gitlab-Token` header, compared in constant time) and in the repository's `hooks/post-receive`:

  ```sh
  #!/bin/sh
  curl -fsS -X POST -H "X-Gitlab-Token: $LAUNCHWAY_HOOK_SECRET" -H "X-Gitlab-Event: Push Hook" \
    https://launchway.example.com/api/v1/projects/shop/git-hook >/dev/null
  ```

  The call only makes the server poll the Flight now; the poll decides what starts, as always, so a forged or repeated call can at most cause a poll.
- **Tags are made with Git.** Launchway makes the annotated tag with its own git and pushes that one tag; a tag already at that commit is accepted, one that points elsewhere is refused. It fetches the branches into a scratch directory to have the commit, so a very large repository takes a while.
- **Releases: the server has none.** The capability is declared **none**, as for Bitbucket and Azure DevOps: **Cut a release** pushes the tag, and the release page and its files stay in Launchway, which serves the files.
- **File changes** are a shallow clone of the branch, the file written (never through a link, never outside the working tree), one commit by the Launchway identity, and a push that is never forced, to the new branch (made from the base branch, or added to when it exists) or to the branch itself. The same content again is not an error. A server that protects the branch refuses the push in its own words, and Launchway returns that message unchanged.
- **No pull requests.** The server has none, so `write_back.merge: pull_request` is refused **before anything is pushed**, with the way around it: use write-back mode `direct` to a branch the account may push to (a branch a person merges on the server), or connect a host that has pull requests. A file change that asks for a pull request pushes its branch and reports that a person must merge it.
- **Results are shown in Launchway only.** A Git server has no commit status and no pull request to comment on, so nothing is written back and the Flight's page says "read results in Launchway".
- **Identity is emulated** (the user name the connection sends; the credential is proved against the repository), **repositories are entered, not listed**, content is read from Launchway's own clone, and creating repositories, reviews, landing, rules and forks are declared none, each with its reason on the Flight's Code host page.

This provider is labelled experimental until it has run against a real server of each common kind. The opt-in test is read-only: `LAUNCHWAY_LIVE_ANYGIT_CREDENTIAL` (a token, or the JSON document), `LAUNCHWAY_LIVE_ANYGIT_BASE` (the server's https address) and `LAUNCHWAY_LIVE_ANYGIT_REPO` (`owner/name`) run it. The strict fake serves a real bare repository over Git's smart HTTP protocol with git's own `http-backend`, requires the Authorization header on every request and can refuse a push with a hook's own message, so the credential, the tag push, the file change and the protected-branch refusal are proved in the unit tests; only a real server's own answers cannot be.

## In the Logbook

`outside_host.create`, `outside_host.delete`, `outside_host.test`, `project.outside_attach`, `project.outside_detach`, `outside_host.status`, `outside_host.comment`, `outside_host.release` and `webhook.outside` name the connection and the person or run. See [Events and the Logbook](../reference/events.md).
