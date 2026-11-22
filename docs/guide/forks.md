# Forks

A **fork** is your own copy of a repository hosted by Launchway. You make changes in it without asking anyone,
then offer them back to the repository you forked from with a pull request. Everything below works the same in
the HTTP API, with the MCP tools and with the `launchway repos` and `launchway pr` commands.

Git hosting must be on (`forge.hosting`).

## Making a fork

```sh
launchway repos fork acme/shop                       # into your own space, as you/shop
launchway repos fork acme/shop --owner team --name shop-experiments --visibility private
launchway repos forks acme/shop                      # the forks you may read
launchway repos fork-status you/shop                 # parent, forks, and how far you are from the parent
```

You need read access to the repository you fork, and the admin role on the path the fork will have
(`owner/name`). An administrator of every Flight may fork anywhere; an installation that wants everyone to have
a space of their own binds each person to `<their name>/*`. A token limited to repositories cannot fork: it is
bound to the repositories it names, and a fork is a new one.

The fork starts with the parent's branches and tags and all their history. It is made in one step: it is
either there whole or not at all, and a person who forks twice in a row gets two independent copies. A name
already in use is refused (`409`). Making forks is rate limited: 20 an hour for one person and 300 an hour
for the server.

The fork remembers its **parent**, its **root** (the repository at the top of the chain, for a fork of a fork),
the **fork point** (the commit of the parent's default branch it was made at), who made it and when. The
repository's page and `GET /api/v1/repositories/{owner}/{name}` show them in a `fork` block.

## Who may fork, and how visible a fork is

Two switches decide whether a repository can be forked, and the nearest one wins:

1. the repository's own switch (`allow_forks`: `on`, `off` or `default`), set by its administrators
   with `launchway repos fork-settings OWNER/NAME --allow-forks on` (or `PATCH /api/v1/repositories/{owner}/{name}`);
2. its owner's switch for all its repositories, set by an administrator with
   `launchway repos fork-settings --owner acme --allow-forks off`;
3. the default: **on for a public repository, off for a private or internal one**.

A refusal says which switch decided.

A fork is **never more visible than its parent**: a fork of a private repository is private, and asking for
`public` is refused. A fork can be narrower than its parent.

### What a fork can read

A fork never widens access to the parent's contents. It gets exactly the objects that were in the parent when
it was made, which the person forking could already read. Commits pushed to the parent afterwards are never
reachable through the fork, however the fork is read or the servers' caches are rebuilt. Only an explicit
**sync** brings them, and a sync is refused when the fork is more visible than the parent (for example after
the parent became private).

Behind the scenes a fork shares the parent's stored packs rather than copying them, so a fork costs a row per
pack instead of a second copy of the history. A stored pack is deleted only when no repository still needs it,
so deleting, purging or garbage-collecting the parent, or the fork, never takes something the other still uses.
The storage counts the shared packs towards the fork's size limit.

### When the parent changes or goes

- **The parent becomes private**: the forks keep what they have and their own visibility. They can no longer
  sync from it while they are more visible than it. New forks follow the new rules.
- **The parent turns forks off**: no new forks; existing ones are untouched.
- **The parent is deleted** (moved to the trash, or purged): the forks stay whole and say so
  (`parent_deleted`); syncing is refused with a sentence. Restoring the parent from the trash restores the link.
- **A fork is deleted**: the parent is untouched. Open pull requests from that fork keep their head (it was
  copied into the target) and can still be merged; their branch can no longer be updated.

## Keeping a fork current

```sh
launchway repos sync you/shop                  # fast-forward the default branch to the parent's
launchway repos sync you/shop --branch fix --upstream main --merge
```

A fast-forward is done when your branch has no commits of its own; when it has, the answer is **diverged** and
nothing changes, unless you ask for `--merge`, which makes a merge commit by you. A merge that conflicts changes
nothing and lists the files (`409`, `conflicting_files`); resolve them on your computer. Only the parent's one
branch is brought over, never its other branches. You need write access to the fork and read access to the
parent. The branch's protection applies as to any push.

`launchway repos fork-status` and `GET /api/v1/repositories/{owner}/{name}/fork` show how many commits the
fork's default branch is **ahead** of and **behind** the parent's.

## Pull requests from a fork

```sh
launchway pr create --head you:fix-retry --title "Add the retry button" -R acme/shop --allow-edits
```

`--head you:fix-retry` names the branch of your fork (`owner/name:branch` for a fork with another name). You can
offer a branch from a fork of the repository, from its parent, or from another fork of the same root.
Opening one needs read access to both repositories, and when the fork is *less* visible than the target, write
access to the fork: only the people who own a branch may offer it to a wider audience.

The pull request lives in the **target** repository. Opening it copies the head commit, and only the objects
that commit needs and the target lacks, into the target under `refs/pull/N/head`. Nothing else of your fork
comes along: not its other branches, not their commits. When you push to the branch again the head follows.
The diff, the compare view, mergeability, reviews, the merge queue and merging all work as for any pull
request, in the target; merging never deletes the branch in your fork.

### Allow edits by maintainers

With `--allow-edits` (or `launchway pr maintainer-edits N --allow`), the people who may write to the target
repository may update your branch in your fork from Launchway: **update branch** and **resolve conflicts**,
as far as the branch's protection in your fork allows. It is off by default. Pushing with Git to your fork
still needs write access to your fork. A fork that is more visible than the target is never written to with
the target's commits.

### Pipelines of fork pull requests

A pull request from a fork runs code nobody on the team has read. Its pipeline runs **restricted**, as
described in [Pull-request trust](pull-request-trust.md): no secrets, no launches (so no deploy lease), no
saved caches, nothing pushed to the Depot, and the pipeline file of the **default branch**, not the one the pull
request brings. The runner checks the pull request out from `refs/pull/N/head` in the target with the run's
read credential for the target; it has no credential for the fork and cannot see the fork's other branches.

A **first-time contributor's** pull request does not run at all until a person with write access to the
target approves it: `launchway pr approve-run N` (or the API or MCP tool). The approval is for the head as it
is then; a new push waits again. An author who already has a pull request merged in the target needs no
approval. Approving lets it run restricted; **Run with secrets** is a separate decision, made on the Sortie.

## Reference

- API: `POST /api/v1/repositories/{owner}/{name}/forks`, `GET .../forks`, `GET .../fork`, `POST .../sync`,
  `POST .../pulls/from-fork`, `PUT .../pulls/{number}/maintainer-edits`, `POST .../pulls/{number}/approve-run`,
  `GET` and `PUT /api/v1/owners/{owner}/fork-settings` (see the [HTTP API](../reference/http-api.md)).
- MCP: `fork_repository`, `list_forks`, `get_fork_status`, `sync_fork`, `fork_settings`, `create_pull_request`
  (with `head` as `owner:branch`), `set_maintainer_edits`, `approve_pull_run`.
- Logbook: `repository.fork`, `repository.sync`, `owner.fork_settings`, `pull.create`, `pull.maintainer_edits`,
  `pull.approve_run`.
- Not built yet: the Fork button and fork line in the web interface, and "from fork" on the pull request page.
