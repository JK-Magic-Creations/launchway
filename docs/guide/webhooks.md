# Webhooks and events

A repository can tell other systems when something happens to it: a push, a branch or tag created or
deleted, the repository created, renamed or deleted. You add a **webhook** to one repository, or to an
**owner** to cover every repository the owner has, and Launchway sends each event to the address you give
as a signed JSON request.

Pipelines do **not** use webhooks. A push to a hosted repository makes the projects that watch it look at
once, inside the server, and starts the pipeline directly (see [Pipelines start on a push](#pipelines-start-on-a-push)).

## Events

| Event | When | Notes |
|---|---|---|
| `push` | A branch or tag moved, or was created | One per ref, even when one push moves several |
| `create` | A branch or tag was created | Sent with the first `push` of that ref |
| `delete` | A branch or tag was deleted | |
| `repository` | The repository was created, renamed (including a move to another owner) or deleted | `action` says which |
| `pull_request` | A pull request changes | `action` says how: `opened`, `edited`, `synchronize` (new commits), `closed`, `reopened`, `merged`, `converted_to_draft`, `ready_for_review`, `review_requested`, `commented`, `auto_merge_enabled`, `auto_merge_disabled`; `number` and `pull_request` say which one |
| `pull_request_review` | A review is submitted or dismissed | `action` is `review_submitted` or `review_dismissed`; `changes` has the verdict or the reason |
| `work_item` | Work changes: an item is assigned to an agent, an agent asks for review or hands it back, or an item moves | `action` is `agent.assigned`, `agent.review_requested`, `agent.handed_back` or `state`; `work` has the item's `ref`, `title` and `state` and the details. See [work item events](../reference/events.md#work-item-events) |
| `ping` | You asked for a test delivery | Always delivered, whatever the hook chose |

## Adding one

In the web UI: **Repository settings, Webhooks** for one repository, or the owner's settings for an owner.
From the command line:

```sh
launchway repos hooks add acme/shop --url https://ci.example.com/hook --events push,create
launchway repos hooks add acme --url https://audit.example.com/hook --events repository   # every repository of acme
launchway repos hooks list acme/shop
```

The API is `POST /api/v1/repositories/{owner}/{name}/hooks` and `POST /api/v1/owners/{owner}/hooks`, and the
MCP tools are `add_webhook`, `list_webhooks`, `update_webhook`, `delete_webhook`, `test_webhook`,
`list_webhook_deliveries` and `redeliver_webhook`. Repository administrators manage a repository's hooks;
an administrator manages an owner's.

### The secret

Each hook has a **secret** that signs every delivery. You can give one (with `--secret-file`, never on the
command line), or leave it out and Launchway makes one and **shows it once**, in the answer that creates the
hook. The secret is stored sealed under the installation's root key ([Encryption at rest](encryption-at-rest.md)); a server with no root key refuses to create a hook (`409`, code `no_root_key`, with how to make a key). After that the secret is never shown again: lists and logs say only that the hook has one. It is
stored sealed in the database. Replace it with `update` when you need to.

## What a delivery looks like

A delivery is an HTTP `POST` of JSON with these headers:

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `User-Agent` | `Launchway-Hookshot` |
| `X-Launchway-Event` | `push`, `create`, `delete`, `repository` or `ping` |
| `X-Launchway-Delivery` | A unique ID; the same ID is in the body, and a replay repeats it |
| `X-Launchway-Signature-256` | `sha256=` and the hex HMAC-SHA256 of the **raw body** under the secret |
| `X-Hub-Signature-256` | The same value, for receivers that already read that header |
| `X-GitHub-Event` | The event's name again, for receivers written for GitHub's format |

Verify it by computing the HMAC over the bytes you received and comparing in constant time:

```python
import hmac, hashlib
def valid(secret: bytes, body: bytes, header: str) -> bool:
    want = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, header)
```

A push:

```json
{
  "event": "push",
  "delivery": "5b1f0c1e-3f55-8d6c-2a41-0e9c7f66b1d2",
  "at": "2026-11-21T10:15:30Z",
  "repository": {
    "path": "acme/shop", "owner": "acme", "name": "shop", "default_branch": "main", "visibility": "private",
    "clone_https": "https://launchway.example.com/git/acme/shop.git",
    "clone_ssh": "ssh://git@launchway.example.com:2222/acme/shop.git"
  },
  "sender": { "name": "ada" },
  "ref": "refs/heads/main",
  "before": "9c2d0f6d0b1a5d8f4e3c7a6b5d4c3b2a19087766",
  "after": "e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4"
}
```

A new branch adds `"created": true` (and a `create` event with `"ref_type": "branch"`); a deleted one is a
`delete` event with `"deleted": true` and an `after` of all zeros. A repository event:

```json
{
  "event": "repository", "delivery": "…", "at": "2026-11-21T10:20:00Z",
  "repository": { "path": "acme/store", "owner": "acme", "name": "store" },
  "sender": { "name": "ada" },
  "action": "renamed",
  "changes": { "old_path": "acme/shop" }
}
```

`action` is `created`, `renamed` or `deleted`. The ping carries a short `zen` line.

## Retries, the log and turning off

Any answer from 200 to 299 is a success. Anything else (another status, a timeout after 10 seconds, a
refused connection) is retried: up to 5 attempts, waiting 10 seconds, 1 minute, 5 minutes and 30 minutes
between them. Redirects are **not** followed: use the final address.

Every hook has a **delivery log**: each delivery's event, state (pending, delivered, failed), number of
attempts, last response status, time taken, the problem in words, and the request that was sent. The log
keeps the newest 200 deliveries. From the log you can:

- **Send a test delivery** (a `ping`), once, now. It shows what your address answered.
- **Redeliver** any earlier delivery: the same body, once, now, as a new delivery marked as a redelivery.
  Tests and redeliveries do not count against the hook.

When **5 deliveries in a row fail for good** (after their retries), Launchway **turns the hook off**,
records `webhook.disable` in the Logbook and shows this notice on the hook:

> Turned off automatically after 5 deliveries in a row failed. The last problem: … Fix the address, then turn the webhook on again.

Turning it on again (the toggle, or `launchway repos hooks update ... --active`) clears the notice and the
count.

## Which addresses are allowed

Deliveries use the installation's **egress allow-list** (`egress.allow` in the Base settings). An
address that is internal (a private network, a loopback or link-local address) is refused unless the
installation allows it, and the delivery says so in words:

> Launchway may not connect to that address: it is an internal address and the installation does not allow it (egress.allow).

The check is made when the connection is made, on the address actually connected to, so a name that
changes its answer cannot get around it. Addresses with a user name or password in them are refused when
you add the hook; the secret signs the delivery instead.

Many chat services put their token in the path of the address, so Launchway treats the address as a
secret: with a root key it is sealed in the database (see [encryption at rest](encryption-at-rest.md)),
and it is shown with the host only (`https://hooks.chat.example/...`) in the interface, the API and
the command line. To change the address, enter the whole of it again. The start of the receiver's
answer, which the delivery log keeps, has token-like strings and Authorization and Cookie values
removed before it is stored.

## Pipelines start on a push

When a push commits, over HTTPS or SSH, Launchway makes every project whose address is that repository
poll at once. The poll finds the moved ref and starts the pipeline through the same path as every other Git
host, so everything that applies to a push applies here:

- **Exactly one run** per push, whichever servers are running; the run records the branch, the commit, the
  author and the commit time.
- `supersede: true` cancels the older runs of the same branch.
- The pipeline's `on: push` filter by branch or tag decides whether to run; a push it ignores is recorded
  with the reason.
- A commit whose subject contains `[skip ci]` (also `[ci skip]`, `[no ci]`, `[skip pipeline]` or
  `[skip launchway]`, in any case) starts no run, and the decision says so. Start one by hand if you want it.

The trigger language has path filters (`paths` and `paths_ignore`) and a step can say `when: { changed }`; see [Queues, limits and path filters](queues.md).

## Audit

`webhook.add`, `webhook.update`, `webhook.delete`, `webhook.test`, `webhook.redeliver` (who, which hook,
which address) and `webhook.disable` (by `system`) are in the Logbook. The secret is never written there.

## Telling Argo CD about a commit at once

Argo CD polls Git every three minutes unless it is told. A deploy step that writes to the GitOps
repository already asks Argo CD to refresh the application it deployed (the refresh annotation, on by
default; it needs the right to patch that Application). For changes made by anyone else (a person
pushing to the GitOps repository), add a webhook to the repository that sends **push** events to
`https://<argocd>/api/webhook`, with the secret you set as `webhook.github.secret` in Argo CD's `argocd-secret`.
Argo CD's receiver accepts the formats of several Git hosts, GitHub's among them; Launchway's push
payload carries GitHub's names (`ref`, `before`, `after`, `repository.html_url`, `clone_url`, `ssh_url`) and
headers (`X-GitHub-Event: push`, `X-Hub-Signature-256`, and `X-Hub-Signature`, the same signature as HMAC-SHA1, for receivers built on an older version of the library that read only that one), so it is read as GitHub's. Argo CD matches the
repository's address (`repository.html_url`) with the applications' `repoURL`: it must be the same address, and it is the one `launchway repos get` shows as the clone address, with the `/git/` before the owner (`https://<launchway>/git/acme/gitops.git`; the `.git` and a trailing slash do not matter). An application whose `repoURL` is the short form without `/git/` still clones, but is not refreshed by the webhook. Argo CD
refreshes the applications of that repository and branch within seconds. If the receiver does not accept it,
polling and the deploy step's refresh remain the supported path.
