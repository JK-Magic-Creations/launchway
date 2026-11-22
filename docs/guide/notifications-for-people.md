---
title: Notifications for people
---

# Notifications for people

Launchway tells people about what concerns them: in one **inbox**, and, if they choose and an administrator has set up a mail server, by **e-mail**. This page is for the people who receive notices and the administrators who set up the mail. The channels that carry alerts to chat services and alert hubs are a different thing: see [Notifications](notifications.md).

## What you are told of

One model covers all of these:

| When | Kind | By default |
|---|---|---|
| A review is asked of you | `review_requested` | inbox and e-mail |
| You are mentioned in a pull request or work item | `mention` | inbox and e-mail |
| An approval is waiting for you (you are a named approver) | `approval_waiting` | inbox and e-mail |
| A credential of yours (an SSH key or a signing key) ends within two weeks | `credential_expiring` | inbox and e-mail |
| A review is submitted on your pull request | `review_submitted` | inbox |
| A comment on a pull request or work item you take part in | `comment` | inbox |
| A pull request you take part in is merged, or closed | `pull_merged`, `pull_closed` | inbox |
| A run of yours failed, or was fixed | `run_failed`, `run_fixed` | inbox |
| A deployment you approved finished, or was returned to base | `deploy_finished`, `deploy_rolled_back` | inbox |
| A release is published in a Flight you watch | `release_published` | inbox |

You are never told of what you did yourself.

## The inbox

The **Inbox** in the top bar, `launchway inbox` and the MCP tool `get_inbox` show one list that unifies what used to be separate: the notices above, the work inbox (assignments, unblocked items, agent work) and the pull requests that wait on you (a review asked of you or your group, changes asked on your own, one the merge queue took out). Each notice can be marked read or done, and the list filters by state (open, unread, done), source and kind. The count in the top bar is the unread entries plus the pull requests waiting on you. A pull request that waits on you leaves the list when it is dealt with; it cannot be marked.

## Your choices

Settings, **Notifications** (or `launchway notices prefs`):

- **Per kind:** in the inbox only, in the inbox and by e-mail, or off.
- **Per repository and Flight:** a watch level. *Participating* (the default) tells you of what you take part in. *All* also tells you of all activity there (and of a release published in that Flight). *None* mutes the repository or Flight entirely. Write the scope as `repo:owner/name` or `flight:name`.
- **Quiet hours:** e-mail that would arrive during them waits and is sent when they end. The inbox is not held. Hours are in your time zone and may cross midnight.
- **Daily digest:** one e-mail a day, at the hour you choose, instead of one for each notice.

E-mail goes to the address in your profile as of your last sign-in.

## What mail carries, and why it carries less

Mail leaves the installation's trust boundary: it passes through mail servers and sits in mailboxes that Launchway cannot protect or recall. So mail carries less than the inbox.

- For a repository that is **not public**, the subject names only the repository and what happened (`[acme/shop] review requested`), and the body holds a link. It never carries a title, a comment or code. For a Flight, run or deployment, the same: its name and a link.
- For a **public** repository, the subject and body carry the title.
- An administrator may allow titles and comments of non-public repositories in mail with `mail.include_private_details`. It is off by default.
- Messages have a plain-text part and a simple HTML part. They contain no images, styles, scripts or other remote content, so opening one fetches nothing.
- Every link in a message goes to the installation's public address (`mail.public_url`); without one, messages carry no links.
- Every message has a **mute link** and the one-click `List-Unsubscribe` header. The link is signed for one person and one kind and needs no sign-in: opening it shows a page with a button, and pressing the button stops mail of that kind (it still appears in the inbox). A mail scanner that opens the link does not mute anything. Change your choices again in Settings, Notifications.

## For administrators: turning mail on

Set the `mail` section of the settings (see [the reference](../reference/server-config.md#mail), and `config.mail` in the [Helm values](../reference/helm-values.md)):

```yaml
mail:
  host: smtp.example.com
  from: "Launchway <notify@example.com>"
  public_url: https://launchway.example.com
  username: { store: vault, key: mail/launchway, field: user }
  password: { store: vault, key: mail/launchway, field: password }
```

- **Encryption is required.** The default is STARTTLS (port 587): the connection is encrypted before anything is sent or any password is offered, and a server that does not offer STARTTLS is refused. `security: tls` encrypts from the first byte (port 465). `security: none` needs `allow_plain: true`, to say that unencrypted mail is acceptable on this network.
- **Credentials are references** to a secret store (`ai.secrets`), read when a message is sent and never stored, logged or shown.
- **The mail server passes the egress guard.** Launchway reaches it as it reaches every address an administrator names: a public host is allowed; a host on an internal network must be listed under `egress.allow`. On Kubernetes the chart's network policy allows HTTPS out only, so add the mail port under `networkPolicy.server.extraEgress`.
- **Send a test message:** Settings, Notifications (administrators see the mail panel), `launchway notices mail-test`, or the MCP tool `send_test_mail`. It says how it went.
- **Limits and retries:** the whole installation sends at most `rate_per_minute` (30) and `rate_per_hour` (300) messages; the rest wait in a bounded outbox in the database (5000 messages). A message that fails is tried again with growing pauses, up to `max_attempts` (5), and then given up on. A message's text is deleted from the outbox as soon as it is sent or given up on.
- **Audit:** every message sent, retried or given up on is in the Logbook (`mail.sent`, `mail.retry`, `mail.failed`) with the person and the kind, never the subject, the address or the body. The test message and each mute are audited too.

A person without a known address (no profile e-mail, or no sign-in yet) simply gets the inbox.

## What is not built yet

Replying to a notice by e-mail, a warning for API tokens that are about to end (SSH keys and signing keys are covered), and notices for the rest of the product's events (these come as the features that produce them are built). Comment text is not carried in a notice; the notice names the pull request and links to it.
