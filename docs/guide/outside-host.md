# Keep your code on another host

Hosting repositories in Launchway is optional. A Flight (codebase) can keep its code on a Git host you already use: Launchway holds a **connection** to that host, a narrow service-account token kept as a reference to a secret, and through it your Sorties run on that code, report back to it and write to it. The host is the primary; Launchway never needs a copy it owns.

This page covers the host kinds `github` and `gitlab` (each the public service and self-managed installs of it: give the install's address as the base address) `gitea` and `forgejo` (self-managed installs: the base address is required) `bitbucket` (Bitbucket Cloud) and `azuredevops` (the cloud service and the self-managed server product). The sections up to "The limits" describe GitHub; [GitLab](#gitlab) [Gitea and Forgejo](#gitea-and-forgejo) [Bitbucket Cloud](#bitbucket-cloud) and [Azure DevOps](#azure-devops) follow, each with its own token scopes and roles. Every kind passes the same conformance suite and the same pages work for all of them.

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

The kind `bitbucket` is **Bitbucket Cloud** (`https://bitbucket.org`, API at `https://api.bitbucket.org/2.0`). **Bitbucket Data Center** (the self-managed product) is **not covered**: its API (`/rest/api/1.0`) and its webhooks are different, and a base address other than `https://bitbucket.org` is refused with that message. A repository is `workspace/name`; the **owners** list names workspaces, and with it Launchway lists those workspaces directly (without it, the account's own workspaces are asked, which needs the `account` scope).

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

## Not yet: `codecommit`

The kind `codecommit` is named in the model, but **no code path offers it**: it has no provider, no strict fake and no conformance run, so a connection of it is refused ("a connection to keep code on is of kind ...") and it is not in the kind picker, the command line or the tools. A host kind is either complete or absent in Launchway. Until it is built, keep such a repository as a plain Git address (polled, with a credential reference) or mirror it into Launchway.

## In the Logbook

`outside_host.create`, `outside_host.delete`, `outside_host.test`, `project.outside_attach`, `project.outside_detach`, `outside_host.status`, `outside_host.comment`, `outside_host.release` and `webhook.outside` name the connection and the person or run. See [Events and the Logbook](../reference/events.md).
