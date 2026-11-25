---
title: Verified commits and tags
---

# Verified commits and tags

Launchway checks the signatures on the commits and annotated tags of the repositories it hosts and says, for each, whether it can be tied to a person. A rule can then ask that only verified commits reach a branch.

## Sign your commits

Launchway verifies **SSH signatures**, the kind Git makes with `gpg.format ssh`:

```sh
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global commit.gpgsign true
git config --global tag.gpgsign true
```

Then register the **public** key: **Settings → People and access → Signing keys** (`/settings/access/signing-keys`), `launchway signing keys add --file ~/.ssh/id_ed25519.pub`, or the MCP tool `add_signing_key`. A signing key is not the same as an [SSH key](git-over-ssh.md) you push with (though the same key may serve both). A key belongs to one person. It may have an expiry date.

OpenPGP signatures (the ones `git commit -S` makes with gpg) are checked too: paste the public key block (`gpg --armor --export <id>`) under Signing keys. RSA (3072 bits or more), ECDSA and Ed25519 keys are accepted, with their signing subkeys; a signature made by a subkey counts for the key it belongs to. A key that has expired or is revoked in its own data shows as *Key expired* or *Key revoked* for what was signed after, and is refused when pasted. Private keys are refused. X.509 (S/MIME) signatures are *Not checked* and never count as verified.

## What each state means

| State | Meaning |
|---|---|
| **Verified** | The signature matches the object, and was made by a key a person registered that was neither revoked nor expired at the time of the commit. The result says whether the signer is also the author or the committer. |
| **Unknown key** | The signature is good, but nobody registered the key, so it cannot be tied to a person. |
| **Bad signature** | The object was changed after it was signed, or the signature is damaged. Do not trust it. |
| **Unsigned** | No signature. Anyone can write any name in the author line. |
| **Key revoked** | The key was revoked, and the object is dated after the revocation. |
| **Key expired** | The key had expired before the object's date. |
| **Not checked** | Signed with an X.509 certificate, which Launchway does not check. |

The *time* of a commit is the date its author wrote into it, which Git lets anyone choose. That is why revocation is "from now on": commits dated before a key was revoked stay verified. If a key was stolen, **delete** it instead: everything it signed then shows as signed by an unknown key.

## Where it is shown

On the commit list, the commit page, the tags page and the Commits tab of a pull request, as a mark that explains itself when you click it; in `launchway signing verify OWNER/NAME OBJECT...`, the MCP tool `verify_signatures` and `GET /api/v1/repositories/{owner}/{name}/signatures?shas=...`. The cryptography is done once for each object and remembered; who owns the key and whether it was revoked is judged again each time.

## Rules

In **Merge rules** for a branch (or `PUT .../merge-rules` with these fields):

- **`require_signed_commits`:** a pull request can be merged only if every commit the merge brings is verified. A *merge* or *rebase* brings the pull request's own commits; a *squash* brings only the new commit Launchway makes. The merge refusal names the commits that are not verified and what to do.
- **`require_verified_push`:** a push to the branch (over HTTPS or SSH, by anyone, administrators too) is refused if it brings a commit that is not verified. The refusal lists the commits and says how to sign them.

Launchway signs the merge, squash, rebase, revert and suggestion commits it makes with an **installation signing key**, so these rules do not block the product's own work. The key is made the first time it is needed and its private half is stored sealed under the installation's root key, like the other installation secrets. Without a root key it cannot be made, and a merge into a branch that requires verified commits is refused saying so ([create a root key](encryption-at-rest.md)). Commits made by editing a file in the web page are not signed yet.

The public half is published at `/.well-known/launchway/signing-key.pub` and by `launchway signing installation-key`. To check the product's commits with Git:

```sh
curl -s https://launchway.example.com/.well-known/launchway/signing-key.pub | sed 's/^/launchway /' >> ~/.config/git/allowed_signers
git config gpg.ssh.allowedSignersFile ~/.config/git/allowed_signers
git verify-commit <commit>
```
