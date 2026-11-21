# Pull requests and code review

A **pull request** is a change traveling from one branch of a hosted repository to another. It has a
number in its repository, a title, a description in Markdown, a conversation, reviews, the results of the
pipelines that ran on it, and rules that say when it may land. Everything below works the same in the web
interface, over the HTTP API, with the MCP tools and with the `launchway pr` command.

Git hosting must be on (`forge.hosting`). Pull requests from a fork are not available yet: the source is
a branch of the same repository.

## Opening one

Push a branch, then open the pull request:

```sh
git push origin my-change
launchway pr create --title "Add the retry button" --body-file notes.md --reviewer grace
launchway pr list
```

A push that creates or moves a branch with no open pull request prints one line, the same over HTTPS and
over SSH, with the address that opens one:

```text
remote: Open a pull request for my-change: https://launchway.example/repositories/acme/shop/pulls/new?head=my-change (or push again with -o pr.create)
```

Git push options open it as the push finishes, with no second step:

```sh
git push -o pr.create -o pr.title="Add the retry button" -o pr.target=main origin my-change
```

`pr.create` opens a pull request from the pushed branch into `pr.target` (the default branch if left out), titled
`pr.title` (the branch name, spelled out, if left out). It needs a person's credential, not a deploy key or a
run's, and is recorded in the audit trail as `pull.create` with the source `git push`. The pull request is made
after git has finished, so if it cannot be (there is nothing to merge, say), the push still stands and the audit
trail says why. The default branch, a deleted branch and a branch that already has an open pull request print
nothing.

The head of every pull request is also a ref of the repository, `refs/pull/N/head`, so it can be fetched
(`launchway pr checkout N`, or `git fetch origin refs/pull/N/head`) and survives the branch being deleted
or rewritten. Each time the head or the base moves, a **revision** is recorded, with whether it was a
force-push; the earlier heads are kept as `refs/pull/N/rev/K`. Nobody can push to `refs/pull/`: the server
keeps those refs itself.

## Can it merge?

The server works out whether the branches merge with `git merge-tree`, in the background, every time
either branch moves; a request never waits for it. The pull request page shows one of:

- **Merges cleanly**, with how many commits it is behind the target.
- **Has conflicts**, naming the files.
- **Checking**, while the answer is being made (or when the one on record was made for another commit).

## Reading the change

The **files changed** are the difference between the merge base (where the branches diverged) and the head,
so what landed on the target since is not shown as the pull request's own. Each file has its status
(added, modified, deleted, renamed with how similar it is), its additions and deletions, and a binary flag.
Patches are bounded: a file with more than 5,000 changed lines, or a patch over 256 KiB, is held back until
you choose **Load anyway** (`--full`), and no answer is larger than 16 MiB.

**Changes since your last review** compares the head you reviewed with the head now
(`launchway pr diff N --since REVISION`).

## Reviewing

- A **comment** on the conversation, or on a **line or range** of a file at a commit. When the code moves,
  the comment moves with it; when the commented lines themselves changed it is marked **outdated** and keeps
  the place it was written.
- Review comments form **threads** that can be resolved and opened again.
- A comment holding a ```` ```suggestion ```` block is a **suggested change**. Someone with push access can
  apply it as a commit on the source branch (`launchway pr apply N COMMENT`); it fails, saying why, if the
  code under it changed or the branch moved meanwhile.
- A **review** has a verdict: comment, approve or request changes. New commits dismiss approvals when the
  repository's setting says so (on by default); a request for changes stays until it is dismissed with a
  reason or its author approves.
- You can **ask people to review**; mentions with `@name` are carried in the events.

Descriptions and comments are stored exactly as written. Clients render them as Markdown without raw HTML,
with the same sanitizer the rest of the product uses.

## Checks and rules

A pipeline whose `on:` has `pull_request: {}` runs for a pull request of a hosted repository: when it is
opened, when new commits arrive, and when it is reopened. The run carries the pull request number and the
target branch, and its result is shown on the pull request: a check named after the pipeline (`ci`) and one
per step (`ci/test`).

A **protected branch** can carry merge rules. They are set per branch or pattern:

| Rule | Meaning |
|---|---|
| Required approvals | How many people must approve, and the lowest role an approver may have (operator or admin) |
| Required checks | Pipelines or steps that must have passed on the head: `ci`, or `ci/test`; a bare `test` matches a step of that name |
| Up to date | The head must contain the target's tip |
| Resolved threads | Every review thread is resolved |
| No self-approval | The author's own approval does not count |
| Administrator bypass | An administrator may merge past the rules, saying why; the reason and what was waived are recorded on the pull request and in the audit trail |

Rules and the repository's other settings (which merge methods are allowed, deleting the source branch after a
merge, dismissing approvals when new commits arrive) are set by a repository administrator in the repository's
**Merge rules** tab, with `launchway pr rules` and `launchway pr settings`, or through the HTTP API and the
`set_pull_settings` tool. Every interface reads what is possible from the server's `actions`, so nothing is
offered that would be refused. Labels, assignees and the people asked to review can be changed on the page, and a
review request can be taken back (`launchway pr review N --unrequest grace`).

Signed commits are reserved: a rule cannot ask for them yet, so none pretends to.

When a pull request cannot merge, the server says why in one sentence, and every screen shows that
sentence instead of a button that would not work:

> needs 1 more approval; `test` has not passed

> it has conflicts in `src/app.go`; update the branch and resolve them

> it is 3 commits behind main and must be up to date; update the branch

## Merging

A repository allows any of three methods, and has a default:

- **Merge commit**: a commit with two parents.
- **Squash**: one commit with the pull request's title, description and a `Co-authored-by` line for
  everyone who wrote a commit.
- **Rebase**: the commits replayed one by one on the target, keeping each author, date and message.

The message of a merge or squash follows the repository's template (`{number}`, `{title}`, `{body}`,
`{source}`, `{target}`, `{author}`, `{repository}`). The source branch can be deleted in the same step.

The merge is made on the server through the repository's compare-and-swap: the new commit and the move of
the branch are one transaction that only succeeds if the target is still where the merge started from. If a
push reaches the target in the meantime, the merge is made again on the new tip (up to five times, then it
fails cleanly and says so). A push is never lost and a merge never half-lands.

Other things you can do: **auto-merge** (merge when the rules pass), **close** and **reopen**, change the **target branch**,
mark as **draft** or **ready**, and **revert** a merged pull request, which opens a new pull request that
undoes it.

## Events

Every change is a `pull_request` webhook event (and reviews are `pull_request_review`); see
[Webhooks and events](webhooks.md). Each change made by a request is in the audit trail
(`pull.create`, `pull.update`, `pull.merge`, `pull.review`, ...). Notifications are left as events: whatever
listens decides who is told.

## What is not here yet

Plainly, these do not exist today:

- **Forks.** A pull request's source is a branch of the same repository. There is no way to ask for a
  change from a copy of the repository.
- **Code owners.** No file says who must review which paths, and no rule asks for it. A rule counts approvals,
  not whose.
- **Signed-commit rules.** Commits are not checked for signatures. A rule that asks for them is refused
  rather than ignored.
- **Notifications beyond events and webhooks.** Launchway records every change as an event and sends the
  webhooks you set up; it sends no email, chat message or inbox entry by itself, and has no per-person
  "watching" settings. Whatever listens to the events decides who is told.
- **The compatibility pull request routes.** Tools written for another Git host's API (the routes the acceptance suite lists) cannot yet list or open pull
  requests here; they use Launchway's own routes above.
