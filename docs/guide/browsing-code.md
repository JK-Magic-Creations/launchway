# Browsing code

A hosted repository can be read without cloning it. Open a repository under **Code** and its **Code** tab shows the files at a branch, tag or commit. Everything below works the same in the web interface, over the HTTP API, with the MCP tools and with the `launchway repos` command. Reading asks for the same permission as a clone: anyone who can clone a repository can browse it, and a repository you cannot see is not there.

Git hosting must be on (`forge.hosting`).

## Files and folders

The tree lists each entry with the last commit that changed it. Launchway finds those commits by walking the history of the folder only, newest first, in rounds of 50, 200, 750 and 3000 commits, and stops when every entry has been seen or after 4000 commits or 6 seconds. An entry last changed further back than that shows **older** and the page says so; the History button of the path still finds it. The answer for a commit and a folder never changes, so it is kept. On a repository of 10,000 files and 300 commits the top folder takes about 20 ms and a subfolder about 10 ms on the machine that wrote this page.

The box at the top switches branch or tag by typing; type at least 7 characters of a commit name to open a commit. The address holds the ref and the path in its query (`?ref=feature/x&path=src/main.go`), so a branch name with slashes is never ambiguous.

Below a folder's tree its **README** is shown. It is Markdown turned into page elements, never into HTML: a `<script>` or a raw tag is shown as text. Relative links go to the file in the repository and relative pictures are the repository's own files; a link or picture that climbs out of the repository is dropped.

## One file

A file opens with line numbers and syntax colors. Click a line number for a link to it, shift-click a second for a range (`#L10-L20`), or type a line in the box. **Copy permalink** gives an address pinned to the commit, so it still shows the same lines after the branch moves. **Raw** downloads the file. A very large file (over 1 MiB), a binary and an empty file are said plainly, with a download; a picture is shown as a picture; a symbolic link and a submodule are shown as what they are.

Raw files are never able to run in the product: they are always a download, never sniffed (`X-Content-Type-Options: nosniff`) and served under a policy that forbids scripts and framing (`Content-Security-Policy: sandbox`). A picture is shown only through an image element and only for picture types.

## History, commits and blame

**Commits** lists a branch's history, or the history of one path; a file's history follows it through renames. A commit page shows the message, author, parents, the Sorties (runs) started for that commit and the changed files with their diffs. **Blame** shows who last changed each line, a range of 1000 lines at a time, with **Blame before this change** to follow a line back through earlier commits. Blame is limited to 120 requests a minute for each person.

## Comparing, branches and tags

**Compare** puts two branches or tags side by side: commits ahead and behind, the commits, the files, and a working **Open a pull request** button (or a link to the pull request that already exists). **Branches** lists every branch with its distance from the default branch, its last commit, whether it is protected and its open pull request. Deleting a branch asks first, respects branch protection, is recorded in the Logbook (audit trail) with the commit the branch pointed at, and shows how to bring it back (a **Bring it back** button, or `git push origin <commit>:refs/heads/<name>`). **Tags** lists tags with an annotated tag's message.

## The Overview: activity, languages and contributors

Three answers describe a repository at a glance. They are in the HTTP API, the MCP tools and `launchway repos activity`, `repos languages` and `repos contributors`; the web interface does not show them yet.

**Activity** is one feed, newest first and in pages: pushes, tags, pull requests opened, merged and closed, and the Sorties and releases of the Flights that watch the repository. A repository has its own feed and an owner has one over all of their repositories. Both are checked against the person asking: a repository you cannot read adds nothing to any feed you see, and the owner's feed does not say that it exists. A Sortie or a release appears only if you may read its Flight.

**Languages** are counted over the files at the default branch's head (or a ref you give) by size, using a small built-in list of file extensions. Vendored directories (`vendor/`, `node_modules/` and a few like them), lock files, minified files and generated files are left out. A file `.launchway/languages` in the repository changes that, one line each: `PATTERN exclude`, `PATTERN include` or `PATTERN language=NAME`; a pattern with a slash is matched against the whole path, one without against the file name, a trailing slash means everything below, and the last line that matches decides. The answer names the rules it used and how many files they left out, and is kept for the head commit. A tree of more than 200,000 files is counted up to that many and says so.

**Contributors** are commit counts per author on the default branch over 30 days, 90 days, 365 days or all of the history. They are made by a background job for the head commit, never while the page waits: the first request starts the job and answers `computing`, and a later one gets the answer. The job looks at no more than 50,000 commits; when it stopped there the answer says `at_least` and every count is a lower bound, never presented as complete. Merge commits are not counted, and people are told apart by author address.

## Download a snapshot

The code page has **Download tar.gz** and **Download zip** for the branch, tag or commit you are looking at: the files of that ref, under a folder named for the repository and the ref (`shop-v1.2/`). Anyone who may read the repository may have one; it is streamed, never built in memory or on disk, and recorded in the Logbook with the commit and its size. A ref with more files than the installation allows is refused before the first byte, in words that say how much it is: the limit is the hosting setting `archive_max_mib` (default 512 MiB of files before compression), changed by an administrator with `launchway repos settings --archive-max-mib 1024`, in the API (`PUT /api/v1/hosting-settings`) or with the `set_hosting_settings` tool. A file marked `export-ignore` in `.gitattributes` is left out, as on every Git host. Snapshots are limited to a few a minute per person.

## From the command line

```sh
launchway repos download acme/shop --ref v1.2 --format zip -o shop.zip
launchway repos tree acme/shop src
launchway repos cat acme/shop src/main.go --ref v1.2
launchway repos log acme/shop --path src/main.go
launchway repos show acme/shop 3f2a9c1
launchway repos blame acme/shop src/main.go --from 100 --to 140
launchway repos compare acme/shop --head feature/x
launchway repos branches acme/shop
launchway repos tags acme/shop
```

The MCP tools are `read_repository_file`, `list_repository_commits`, `get_repository_commit`, `get_repository_diff`, `blame_repository_file`, `list_repository_branches`, `list_repository_tags` and `search_repository_code`. Everything a repository holds was written by others; the tools clip it and treat it as data.

## Keys

| Key | Does |
|---|---|
| `t` | find a file by typing part of its path |
| `j` `k` | next and previous entry in a folder |
| `y` | copy the permalink |
| `b` | blame the open file |
| `e` | edit the open file (operators) |

## Limits

Reading is not metered, but blame and search are limited per person. A file over 1 MiB is not shown or blamed in the page. Not yet: cross-repository search, code navigation (go to definition) and releases.
