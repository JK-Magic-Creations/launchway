# Renaming and transferring repositories

Git hosting must be on (`forge.hosting`).

A repository's administrators can rename it or move it to another owner (a transfer): **Settings** of the repository, or `launchway repos update OWNER/NAME --rename-to NEWOWNER/NEWNAME`, `PATCH /api/v1/repositories/{owner}/{name}`, the `update_repository` tool. To move it to another owner you also need the admin role on the new address.

## What keeps working

The address the repository had keeps working until another repository is made, restored or moved to that address:

- **Git over HTTPS and SSH.** A clone, fetch or push through the old address is served from the repository where it is now, and the client is told: pushes and SSH sessions print "This repository moved from acme/shop to acme/store" with the command to change the remote. Credentials are checked against the repository where it is now.
- **The API and web pages** answer `308` with the new address, the rest of the path and the query kept, so a bookmark or a script that follows redirects arrives. Only someone who may read the repository at its new address gets the redirect; anyone else gets the answer for an address that is not there, so an old address never tells a stranger where a private repository went.
- **A Flight whose address is the old clone address** still reads the repository, and releases still tag it.

A repository renamed twice answers at both old addresses.

## What follows the repository

Everything that names the repository by its identity follows it: webhooks of the repository, deploy keys, protected branches and tags, merge rules, mirrors, pull requests, releases, and API tokens limited to it (the list in the token is brought up to date). Webhooks sent for every repository of an owner follow the owner: after a transfer the repository is covered by the new owner's hooks and no longer by the old owner's.

What does not move is access: who may read, operate or administer a repository is configuration kept by owner and name patterns. After a transfer, check that the people who should reach it have access for the new owner. The transfer page names this before you confirm.

## When the old address stops answering

When another repository takes the address (a new one, a restore from the trash, or a rename into it), the old address belongs to that repository from that moment: it is not mapped any more. Tokens, Flights and clones that still use the old address then reach the other repository, or are refused, as for any other address.
