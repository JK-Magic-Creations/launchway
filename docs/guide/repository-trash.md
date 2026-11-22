# Deleted repositories

Git hosting must be on (`forge.hosting`).

Deleting a repository moves it to the trash. It is no longer served or listed and its name can be used again, but nothing is lost: its refs, packs, pull requests, keys and rules wait. The trash records who deleted it and when, and when it will be removed for good.

## See what is in the trash

The **Deleted repositories** page (under Settings) lists the deleted repositories you administer; an administrator of every Flight sees all of them. Each row says who deleted it, when, and on which day it goes for good. On the command line:

```sh
launchway repos trash
```

The API is `GET /api/v1/repository-trash` and the MCP tool is `list_repository_trash`.

## Restore one

Restore brings a repository back with everything it had. Its administrators may do it:

```sh
launchway repos restore rep_8d1f
```

If another repository took the address in the meantime, the restore is refused in plain words and the row says so; give another name:

```sh
launchway repos restore rep_8d1f --name shop-old
```

Restoring under another owner needs an administrator of every Flight. Restores are in the Logbook as `repository.restore`.

## Removal for good

A deleted repository is removed for good when its retention ends. The retention is the hosting setting `trash_retention_days` (default 30 days; change it with `launchway repos settings --trash-days 60`). A background worker does it, on every server, and writes a `repository.purge` line to the Logbook for each repository, under the user `system`.

An administrator of every Flight may remove one at once. The address it had must be typed to confirm, because nothing can bring it back:

```sh
launchway repos purge rep_8d1f --confirm acme/shop
```

What is removed: the repository's refs, its packs and the pack blobs in the blob store, its protected-branch and tag rules, keys, webhooks, mirrors, pull requests and the rest of its own rows. A blob that a pack of another repository still names is never deleted. The removal can stop at any point (a crash, a lost connection) and is finished the next time the worker runs; until its last step the repository is still known to the storage, so an unfinished removal is always found. A removal that has begun cannot be restored.

One thing is not removed: a pack upload that never reached the storage's list (a push that died before it registered its pack) is not named by anything, and the blob store cannot list keys, so such a leftover stays. They are small and rare.
