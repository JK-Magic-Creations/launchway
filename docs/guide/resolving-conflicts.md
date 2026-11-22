# Resolving conflicts without leaving Launchway

When the target branch of a pull request has changed the same lines as the pull request's branch, the pull
request says **conflicts** and lists the files. Launchway can show each conflict as data and make the merge
commit that resolves it, over the HTTP API, with the MCP tools and with `launchway pr`.

This works for a pull request within a repository and for one from a [fork](forks.md). It is a merge of the
**target into the pull request's branch**, like `git merge target` on the branch: **ours** is the pull request's
branch, **theirs** is the target.

## Seeing the conflicts

```sh
launchway pr conflicts 12 -R acme/shop
```

`GET /api/v1/repositories/{owner}/{name}/pulls/{number}/conflicts` answers, for every conflicting file:

- its three sides (`base`, `ours`, `theirs`: whether it exists, mode, size, whether it is binary, and the
  text of a text file up to 1 MiB);
- `merged`: the text as Git leaves it, with conflict markers, and `regions`: the same as data, a list of
  `common` pieces and `conflict` pieces each with `ours`, `base` and `theirs` text;
- its `kind` and the `choices` it takes, in words: `content` (both changed the text), `binary`,
  `delete_modify` (one side deleted what the other changed), `rename` (a rename that clashes), `structure` (a
  file against a folder, or a change of type) and `add_add` (both added a file of that name);
- Git's own sentence for it.

It also gives `head` and `target`, the two tips the answers are for, and says whether *you* may resolve
(`may_resolve`) and, when not, why in plain words.

## Answering

Each conflicting file takes exactly one answer:

| Answer | Meaning |
|---|---|
| `take: "ours"` / `"theirs"` | the version of the branch / of the target (or its deletion, when that side deleted the file) |
| `take: "both"` | text files: ours, then theirs |
| `take: "delete"` | remove the file from the result (rename and structure conflicts) |
| `content` | the whole resolved text of a text file |
| `regions` | an answer for every conflicting region: `ours`, `theirs`, `both` (ours first) or `custom` with `text` |

A binary file, a symbolic link, a submodule or a file over 1 MiB takes a side only. A file text may be at most
1 MiB, and one resolution at most 8 MiB, 200 files and 5,000 regions per file.

Text that still has conflict markers (a `<<<<<<<` line followed later by a `>>>>>>>` line) is refused with the
file named, unless the answer says `keep_markers: true` because the file really contains such text. Every
conflicting file must be answered; the refusal lists the ones left.

```sh
launchway pr resolve 12 -R acme/shop --take theirs                    # every conflicting file
launchway pr resolve 12 --file src/a.go --file docs/b.md --take ours   # these files
```

The command line answers whole files. For regions or text, use the API (`POST .../conflicts/resolve`) or the
MCP tool `resolve_pull_conflicts`:

```json
{"expect_head": "…", "expect_target": "…", "message": "optional",
 "files": [{"path": "a.go", "regions": [{"index": 1, "take": "both"}]},
           {"path": "logo.png", "take": "theirs"}]}
```

`expect_head` and `expect_target` are the tips the conflicts list gave. If either branch moved since, nothing
is done and the answer says so (`409`): list the conflicts again.

The result is **one merge commit** with the branch and the target as parents, authored by you, and the branch
moves to it, as with *update branch*: the branch's protection, the pull request's new head and its checks all
follow. For a pull request from a fork the commit is made in the fork's branch.

## Who may

- A branch of the repository itself: the operator role on it, and the branch's protection (a protected branch
  refuses anyone it is protected against, and says so).
- A branch in a fork: write access to the fork, or the operator role on the target **if the author allowed
  edits by maintainers** on that pull request; the fork's branch protection applies; and the fork must not be
  more visible than the target (the merge copies the target's commits into the fork). Each refusal says which
  of these it is.
- A token limited to repositories cannot change a branch of another repository.

A rebase cannot be answered all at once and is refused with a sentence; do it on your computer. Mergeability
is judged again whenever the target moves, so the pull request page and the list say about a conflict as soon
as it exists.

## Reference

- API: `GET .../pulls/{number}/conflicts`, `POST .../pulls/{number}/conflicts/resolve`.
- MCP: `list_pull_conflicts`, `resolve_pull_conflicts`.
- Logbook: `pull.resolve_conflicts`.
- Not built yet: the editor page in the web interface.
