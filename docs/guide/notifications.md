# Notifications

How Launchway tells people that an approval waits, or that a run, a deployment or a rollback went wrong.

An approval that waits silently is a delivery that stops. Launchway tells you: rules say which events
go to which channels, the messages say in plain words what happened and link to the page where you can act,
and a delivery log shows what was sent, what waits for a retry and what could not be sent, and why.

## Events

| Event | When |
|---|---|
| `approval.requested` | A step waits for a person to approve it (or a paused verification waits to be marked passed or failed) |
| `step.waiting_long` | An approval has waited longer than the rule's `after` (default an hour); told once |
| `release.published` | A release was published (see [Making a release](releases.md)) |
| `release.promoted` | A release was promoted to an Airfield; a promotion past a checklist says so |
| `release.returned` | An Airfield was returned to base |
| `freeze.started` | A freeze began: nothing is promoted until it ends |
| `run.failed` | A run failed. Narrow it with `branches: [main]` to hear only about the branches that matter |
| `deployment.succeeded` | A deployment is done and nothing rolled it back |
| `deployment.failed` | A deployment failed, or its rollback failed too (then the message says the environment needs a person) |
| `deployment.rolled_back` | A failed deployment was rolled back and the environment is healthy again |
| `rollback.needs_review` | An emergency rollback was made and no approver has acknowledged it yet |

## Channels and rules

```yaml
notifications:
  channels:
    - name: alert-hub
      kind: webhook                       # a JSON body, signed
      url:    { store: vault, key: homelab/apps/launchway/notify, field: hub_url }
      secret: { store: vault, key: homelab/apps/launchway/notify, field: hub_signing_key }
    - name: team-chat
      kind: slack                         # also discord and teams
      url: { store: vault, key: homelab/apps/launchway/notify, field: slack_url }
  rules:
    - on: [approval.requested, step.waiting_long, deployment.failed, deployment.rolled_back, rollback.needs_review]
      to: [alert-hub, team-chat]
    - on: [run.failed]
      branches: [main]
      to: [alert-hub]
    - on: [deployment.succeeded]
      environments: [prod]
      projects: ["mcp-*"]
      to: [team-chat]
```

- **A channel's address is a reference, never a value.** The web address of a chat webhook lets anyone post
  to it, so it is read from the server's own secret store (`ai.secrets`) when a message is sent, and is never
  written in the configuration, the log, an error or a message. The same goes for the signing key.
- **Rules apply at each level.** `projects`, `environments` and `branches` take patterns such as `mcp-*`;
  leave them out to cover everything. Several rules may match one event: a channel gets it once.
- **The notifications section is installation configuration**, kept with the other Base settings
  (in the database since schema 23; `launchway config import` reads it from the file).
- **The webhook** sends a JSON body with `event`, `project`, `environment`, `branch`, `run_id`, `run_number`,
  `step`, `title`, `detail`, `link`, `at` and `text` (the same words, ready to show), signed like a repository
  webhook: `X-Launchway-Signature-256: sha256=<HMAC-SHA256 of the body with the key>`. That is what an alert hub
  needs to receive it. Slack, Discord and Microsoft Teams get the text in the shape their incoming webhooks take.
- **Calls go through the egress guard**, like every call the server makes to an address someone wrote: a channel
  inside your network needs its host in `egress.allow`.

## What a message says

Plain words, and where to act:

> Run #12 of mcp-weather waits for you at approve-prod
> Fix the health route. Open the run to approve or reject it.
> https://ci.example.com/runs/run_…?step=approve-prod

Set `reports.origin` so the links are full addresses.

## The delivery log

Every message is a row: event, channel, status (`sent`, `pending` for a retry, or `failed`), attempts, the
reason for the last failure, and when it will be tried again. `launchway notifications`, `GET /api/v1/notifications`
and the MCP tool `list_notifications` show it, for the projects you may read. A failed call is tried again after 1
minute, 5 minutes, 30 minutes and 2 hours (five attempts), then stays as `failed` with its reason ("the channel
answered 500 Internal Server Error", "reading the channel's address (vault:…): …"). A message about one event is
made once per channel, whichever server notices it first (several servers can share a database).

## In the product

What waits for a person is on Home under "Waiting for you": approvals, paused verifications, failed runs and
unhealthy applications. A rollback that needs review is not on that list yet; `rollback.needs_review` tells you
about it.

## Not here yet

Email (Launchway has no mail sender yet; the setting would be a channel of kind `email` with an SMTP server,
and is the next kind to add), per-person subscribe and mute, and the other chat services' own formats beyond
the incoming-webhook presets above.
