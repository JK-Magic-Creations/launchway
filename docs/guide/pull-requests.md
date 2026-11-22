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
run's, and is recorded in the Logbook as `pull.create` with the source `git push`. The pull request is made
after git has finished, so if it cannot be (there is nothing to merge, say), the push still stands and the audit
trail says why. The default branch, a deleted branch and a branch that already has an open pull request print
nothing.

The head of every pull request is also a ref of the repository, `refs/pull/N/head`, so it can be fetched
(`launchway pr checkout N`, or `git fetch origin refs/pull/N/head`) and survives the branch being deleted
or rewritten. Each time the head or the Base moves, a **revision** is recorded, with whether it was a
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
- You can **ask people or groups to review** (`launchway pr review N --request grace`, `--request-group writers`);
  a review by any member of an asked group answers the request. Mentions with `@name` are carried in the events.
- **Several suggestions** can be applied as one commit (`launchway pr apply N COMMENT COMMENT`). All or none are
  applied; two that change the same lines are refused, naming both reviewers, because only a person can say which wins.
- A title that starts with `WIP:` or `Draft:` (or `[WIP]`) blocks a merge like a draft does, and no administrator
  can bypass it. A **draft** cannot merge either; checks still run on it.
- On the **Files changed** tab, tick **Viewed** on a file (key `v`) to fold it away. The mark is yours, is
  remembered, and lapses when that file changes. Press `?` on the page for the list of keys.

### Code owners

A repository says who owns which files in `.launchway/owners`, a plain text file read from the **target branch**
(never from the pull request's own branch, so a change cannot give itself an owner):

```text
# the last line that matches a file decides its owners
*              @ada
/docs/         @vic @group:writers
*.go           @bob @group:backend
/docs/private/             # no owners: unowned again
```

- A pattern with no `/` (apart from a trailing one) matches at any depth; one with a `/` is anchored at the root of
  the repository; a trailing `/` means a folder and everything below it. `*` stays inside a name, `**` crosses
  folders, `?` is one character. There are no negations and no character classes.
- Owners are people (`@name`) and groups (`@group:name`, a group of your sign-in provider or a local one).
  A group owner is satisfied by an approval from anyone who was in that group when they approved.
- Mistakes are said plainly with their line, for example `line 4: "bob" is not an owner: write a person as @name
  and a group as @group:name`. Check a file before you commit it with `launchway pr owners --check FILE`.
  A file with a mistake blocks a rule that asks for owner approval until it is fixed, and cannot be bypassed.
- `launchway pr owners N` (and the file list on the page) shows who owns each changed file.
- A repository setting asks the owners to review by itself when a pull request is ready for review: **every owner**
  of the changed files, or **one owner per file, by turns**, or **one owner per file with the fewest open requests**.
  Groups are always asked as groups. A draft gets nobody until it is marked ready.

Groups here are the groups the server already knows (the sign-in provider's, and the local ones); a Squadron holds
Flights (codebase), not people, so a Squadron cannot be asked to review yet.

Descriptions and comments are stored exactly as written. Clients render them as Markdown without raw HTML,
with the same sanitizer the rest of the product uses.

## Checks and rules

A pipeline whose `on:` has `pull_request: {}` runs for a pull request of a hosted repository: when it is
opened, when new commits arrive, and when it is reopened. The Sortie (run) carries the pull request number and the
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
| Owner approval | For every changed file that has owners, one of them (or a member of an owning group) must approve. The author's own approval never answers for them |
| Latest push approved by someone else | The newest push needs an approval of its own commit from someone other than who pushed it |
| Dismiss stale approvals | New commits dismiss earlier approvals on pull requests into this branch (also set repository-wide) |
| Checks by path | A check that is required only when a changed file matches (`docs/,*.md=docs`). A pipeline that its own path filter kept from running never blocks |
| Merge queue | Merges into the branch go through the merge queue (below) |
| Administrator bypass | An administrator may merge past the rules, saying why; the reason and what was waived are recorded on the pull request and in the Logbook |

Every rule is checked by the server when a pull request is merged or added to the merge queue, whichever way it is asked for:
the web page, the API, the CLI, MCP, auto-merge and the merge queue all get the same answer.

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

## Protected tags

A version tag that people have seen is a promise. A repository's administrators protect tags by pattern, next to the
protected branches, in the repository's **Branches and tags** settings, with `launchway repos protected-tags`, through
the HTTP API (`PUT /api/v1/repositories/{owner}/{name}/protected-tags`) or the `set_protected_tags` tool.

| Setting | Meaning |
|---|---|
| Pattern | The tags covered: `v*`, `release-*`; `*` matches anything but a slash |
| Who may make one | An administrator of the repository (the default) or anyone who may push |
| Who may move or delete one | Nobody (the default) or an administrator |
| Flights | Flights whose own credential may make a matching tag; they can never move or delete one |

The rules hold on every way in: Git over HTTPS, Git over SSH, the tag routes of the API and the tag that **Cut a
release** makes when it publishes. A refusal says why, in the same words on every door, for example: "the tag v1.2.0
is protected (pattern v*) and cannot be deleted: no one may, until an administrator changes the rule; make a new tag
instead". Each change of the rules is in the Logbook as `repository.protect_tags`. A repository with no rule
behaves as before.

## The merge queue

A branch whose rule says **merge queue** takes pull requests only through the merge queue, so what lands has been tested on
the branch as it will be, not as it was when the pull request was last green.

1. A ready pull request is added with **Add to the merge queue** (`launchway pr queue N`). It must meet every rule
   except the checks that have not finished; a required check that already failed refuses it, with the sentence.
2. The merge queue takes one entry at a time, in the order they arrived (an administrator can give one priority). For the
   entry at the front it brings the branch up to date with the target (a merge commit, which is a push, so the
   branch's own checks run on exactly what will land), waits for the required checks of that head, and merges it
   with the method it asked for. The page shows its place and links the Sortie (run) testing it.
3. An entry that fails a required check, conflicts with the branch after the ones ahead of it merged, loses a
   required approval, or gets new commits (a force-push included) while it waits is **taken out with the reason**,
   the person who added it to the merge queue is told (Needs you, and the channels below), and the next entry goes on.
4. Its author or an operator can take it out at any time (`launchway pr queue N --cancel`).

A merge queue never updates a branch that is itself protected: the entry is taken out and says so. A direct merge into a
branch with a merge queue is refused; an administrator can still merge past it, saying why, as with any rule. Auto-merge
adds the pull request to the merge queue instead of merging it.

Not built yet: testing several entries at once, each on top of the ones ahead, and splitting the batch to find
the failing one. Today the merge queue is serial.

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
mark as **draft** or **ready**, **update the branch** (merge the target into it, or rebase it; `launchway pr
update-branch N`), and **revert** a merged pull request, which opens a new pull request that undoes it.

## Who is told

Review requested, approved, changes requested, mentioned and taken out of the merge queue are
**notification events** (`pull.review_requested`, `pull.approved`, `pull.changes_requested`, `pull.mentioned`,
`pull.queue_failed`): name them in a notification rule to send them to a chat or webhook channel. A channel is
shared by the installation, so each message names the people it is about. In the product, the pull requests that wait on
you are in **Needs you** on Home, and `launchway pr inbox` lists them: a review asked of you or your groups,
changes asked on your own pull request, your own taken out of a merge queue.

## Events

Every change is a `pull_request` webhook event (and reviews are `pull_request_review`); see
[Webhooks and events](webhooks.md). Each change made by a request is in the Logbook
(`pull.create`, `pull.update`, `pull.merge`, `pull.review`, ...). Notifications are left as events: whatever
listens decides who is told.

## What is not here yet

Plainly, these do not exist today:

- **Forks.** A pull request's source is a branch of the same repository. There is no way to ask for a
  change from a copy of the repository.
- **Owners that are Squadrons.** Owners and reviewers are people and groups; a Squadron holds Flights, not people.
- **A pending review kept between visits**, comments on a whole file rather than a line, hiding whitespace in the diff,
  a file tree for very large pull requests, and the changes since *your* last review (the changes since a chosen
  revision are there).
- **Pull-request templates, labels with colors, milestones, cherry-picking to another branch** and a summary comment from the checks.
- **Batches in the merge queue.**
- **Signed-commit rules.** Commits are not checked for signatures. A rule that asks for them is refused
  rather than ignored.
- **Email, and per-person "watching" settings.** Chat and webhook channels and the Needs-you list tell people;
  nothing is sent by email, and a channel is the installation's, not a person's.
- **The compatibility pull request routes.** Tools written for another Git host's API (the routes the acceptance suite lists) cannot yet list or open pull
  requests here; they use Launchway's own routes above.
