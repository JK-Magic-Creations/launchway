# Keep your code on another host

Hosting repositories in Launchway is optional. A Flight (codebase) can keep its code on a Git host you already use: Launchway holds a **connection** to that host, a narrow service-account token kept as a reference to a secret, and through it your Sorties run on that code, report back to it and write to it. The host is the primary; Launchway never needs a copy it owns.

This page covers the host kind `github`: the public service, and self-managed installs of it (give the install's address as the base address).

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

## Webhooks or polling

Webhooks start a Sortie at once; polling is the fallback and also the check on them. On the host, add a webhook on the repository (or the owner) with:

- the payload address `https://YOUR-LAUNCHWAY/api/v1/outside-hosts/NAME/webhook`
- the content type `application/json`
- the secret from step 4
- the events: pushes, and pull requests (tag pushes arrive as pushes)

Launchway checks the signature with the connection's secret in constant time, refuses a call it cannot verify (an unknown connection, no secret and a wrong signature all answer `404`, and the failed calls count against the caller's address; a connection also accepts at most 300 calls a minute, and the rest answer `429`), ignores a delivery ID it has seen, and records each accepted delivery in the Logbook. A verified event only makes the Flights on that repository poll at once, so a forged or replayed call can at worst cause a poll. If Launchway cannot be reached from the host, do nothing: the Flight is polled on its schedule, as any Git address is.

A pull request from a fork runs under the usual restricted pull-request rule: no secrets, no deploy steps. The audit line says the Sortie came from a fork.

## The limits

- One token, narrow: an app-installation credential, which would make a short-lived token for each Sortie, is a planned addition; the connection has a place for it and needs no migration to add it. Until then a Station receives the connection's token for the Sortie's checkout.
- The summary comment carries the Sortie's result and step counts. Test and findings counts in the comment are planned.
- A Launch (deploy) writing back to an outside deployment repository: the connection can commit a file and open a pull request, and refuses a direct write to a protected branch, but the deploy step's own push still uses the credential you configured for it. Wiring the step to the connection is planned.
- A connection cannot create repositories, merge, review, or change branch protection.
- Self-managed installs: the API address is the base address plus `/api/v3` and uploads go to `/api/uploads`, as the host documents; give `--base-url` and nothing else unless your install differs.
- This provider is labelled experimental until it has passed the conformance suite against the live service (`LAUNCHWAY_LIVE_GITHUB_TOKEN` and `LAUNCHWAY_LIVE_GITHUB_REPO` run that test).

## In the Logbook

`outside_host.create`, `outside_host.delete`, `outside_host.test`, `project.outside_attach`, `project.outside_detach`, `outside_host.status`, `outside_host.comment`, `outside_host.release` and `webhook.outside` name the connection and the person or run. See [Events and the Logbook](../reference/events.md).
