# GitOps write-back

How a deploy step changes the GitOps repository: several pipelines at once, a tag and a digest together, and a repository that Launchway hosts and protects.

A deploy step with `argocd.write_back` commits the new image to a GitOps repository, and Argo CD
(or any GitOps controller) applies it. This page covers what happens when many pipelines write
the same repository, how the image is written, and how a step writes to a repository that Launchway
hosts, with its branch protected.

## Several pipelines writing one file

Many services often share one manifest (`apps/mcp-servers/base.yaml` holds eight). Deploys of
different projects can then arrive at the same moment, so a deploy runner:

- **Edits only the value it was told to change.** It finds the image references in the YAML, rewrites
  those values in place and leaves every other byte alone, comments, quoting and order included.
  It never writes the file from a copy it read earlier, so it cannot put back an old value of
  another service.
- **Serializes its own writers.** One deploy runner writes to one repository and branch at a time.
- **Starts again on the new tip when the push is rejected.** If another pipeline, or another deploy
  runner, pushed first, the branch has moved. The runner fetches the new tip, applies its edit on top
  of it, and pushes again, waiting a little longer (and a little differently) after each rejection so
  that writers do not collide again. It never forces a push.
- **Gives up in words.** After 12 tries it fails with: "main kept moving on the server: other
  pipelines were writing to it all the time; gave up after 12 tries to push this change on top of
  theirs. Nothing was changed. Run the deploy step again…". Nothing is half written.
- **Finds its own earlier commit.** A step retried after a crash finds the commit it already made
  (by the `Launchway-Deployment` line in its message) and does not make a second one.

The test `TestEightWritersOneFileNoneLost` starts eight writers on eight separate clones of one
repository, each changing its own image in one file at the same moment: all eight changes land,
none is lost, and the file's comments survive.

## A readable tag and a digest

A change gives a `tag`, a `digest`, or both:

```yaml
write_back:
  repo: gitops
  branch: main
  changes:
    - file: apps/jkma/base.yaml
      image: homelab/jkma
      tag:    { output: tag.tag }
      digest: { output: image.digest }     # with a tag as well: image:tag@sha256:...
```

| You give | The reference becomes |
|---|---|
| `tag` | `registry/homelab/jkma:20261121-abcdef12` |
| `digest` | `registry/homelab/jkma@sha256:0123…` |
| both (or `format: tag_and_digest`) | `registry/homelab/jkma:20261121-abcdef12@sha256:0123…` |

The registry resolves a reference by its digest, so the pinned image is the one that was built, and
the tag is there for the people who read the file and the history. Every reference to the image in
the file changes, whether written with a registry host, with a tag or without. A rollback is a `git
revert` of the commit, so it restores both parts exactly. If your chart wants the tag and the
digest in two separate keys, use two `set` changes (`set: image.tag`, `set: image.digest`).

## A repository hosted by Launchway

When the GitOps repository is hosted by Launchway, the deploy step needs no stored token. The
environment says how it writes (`write_back` in the [server configuration](../reference/server-config.md)),
and the server makes a **credential for that one deployment** when it hands the step to a deploy
runner. The credential:

- lasts an hour for pushing (and the step's own deadline for asking about its pull request);
- is made by the server and kept by nobody: nothing is stored, nothing to rotate or leak for later;
- can push to one repository and one branch, never delete, never read another repository, and is not
  an API token;
- is not an administrator's key: the repository's rules decide what it may write.

```yaml
# server configuration
environments:
  - name: prod
    target: homelab
    projects: ["mcp-*"]
    protected: true
    write_back:
      repo: platform/homelab-gitops       # hosted here
      mode: direct                        # or pull_request

# deploy runner configuration
deploy:
  repos:
    gitops:
      url: https://launchway.example.com/git/platform/homelab-gitops.git
      hosted: true                        # write with the step's own credential
      paths:
        - { projects: ["mcp-*"], files: ["apps/mcp-servers/base.yaml"] }
```

A step that names a hosted repository fails, saying so, when the runner's repository is not marked
`hosted` or the environment has no `write_back`: it never falls back to some other credential.

### Mode `direct`: a rule admits the pipeline

The branch stays protected: nobody but an administrator pushes to it. A **pipeline writer rule** of
the repository admits a project's deploy steps to that branch for exactly the files they may write:

```sh
launchway repos protected-branches platform/homelab-gitops --set main \
  --writer 'main=mcp-*=apps/mcp-servers/base.yaml'
```

or `PUT /api/v1/repositories/platform/homelab-gitops/protected-branches` with
`{"patterns":["main"],"pipeline_writers":[{"branch":"main","projects":["mcp-*"],"files":["apps/mcp-servers/base.yaml"]}]}`
(the MCP tool `set_protected_branches` takes the same). When a push arrives the server reads the
files it changes and refuses it unless every one is admitted: "the pipeline of project mcp-weather
may not change README.md on main: its rules admit only apps/mcp-servers/base.yaml". A rule that
admits every file (`**`) is refused when it is saved. A project with no rule gets: "no rule of this
repository admits a pipeline of project shop to main…".

### Mode `pull_request`: the step opens a pull request

The step pushes its change to a branch of its own, `launchway/deploy/<deployment>`, opens a pull
request into the branch, and waits. The repository's merge rules apply as for anyone: required
checks, approvals, and the rest. The environment chooses who merges:

- `merge: auto` (the default): the pull request merges itself as soon as the rules are met, by the
  method of `write_back.method` or the repository's default.
- `merge: person`: it waits for a person to merge it.

The run page's step log shows the pull request's number and address, and, each time it changes,
what the step is waiting for ("needs 1 more approval; the check `test` has not passed", "a person to
merge it"). The step goes on with the commit that landed, then asks Argo CD to sync it. A pull request
closed without merging fails the step ("pull request #12 was closed without being merged, so main was
not changed. Nothing was deployed…"), and so does the step's deadline ("was not merged in time; it
waits for a person to merge it"). A rollback step in the same environment works the same way, in one
pull request.

The pipeline writer rule is needed in this mode as well: it limits which files the branch of the
step may change, so a pull request cannot carry more than the step may write.

Not done yet: a rollback of several deployments at once ("rollback after success") in `pull_request`
mode fails with a sentence saying so; use mode `direct` for that environment.

### A repository hosted elsewhere

Without `write_back` and `hosted`, nothing changes: the deploy runner writes with the credentials of
its own configuration (`deploy.repos.<name>.credentials`), as before.
