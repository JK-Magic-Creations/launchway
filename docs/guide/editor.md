# Editing pipelines

The pipeline editor in the web UI: check a pipeline as you type, run unsaved text as a draft, and save to Git as yourself.

Git is the only place a pipeline is kept. Launchway never holds a copy of its own: the editor
(*Projects → Edit pipeline*, or *Edit this pipeline* on a run's *Pipeline* tab) reads
`.launchway/pipeline.yaml` from the tip of a branch, checks it as you type with the same parser
the server uses, marks problems on their lines and draws the graph. It shows which branch and
commit the text came from and whether you have changed it. Unsaved edits stay in that browser tab
only. A branch without a pipeline opens with a starting point.

**Run this draft** runs the edited text against the branch's current commit without saving it.
The run is marked *Draft* everywhere; it records who started it and the SHA-256 of the text, and
keeps the text itself (which is in no commit) so that its *Pipeline* tab shows exactly what ran.
A draft is its own trigger event, `draft`:

- a step with no `when` runs as usual;
- a step with a `when` runs only if its `event` list names `draft`. `when: { branch: main }` or
  `when: { event: [push, manual] }` does not run in a draft of `main`, so a deployment guarded by
  `when` does not run from unsaved, unreviewed text;
- a step that declares `secrets` fails before any runner sees it, unless the project allows
  drafts to read secrets (below). Its runner never looks a secret up.

A draft lets someone run steps nobody has reviewed, with whatever the runners can reach, so who
may start one is a setting of its own, checked on the server with or without sign-in:

```yaml
drafts:
  allow: operator      # admin, operator (the default) or none: the role needed on a project
  secrets: false       # the default: steps of drafts may not read secrets
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    drafts: { allow: admin, secrets: true }   # overrides, per project
```

Resuming a failed draft run runs its text again, so it needs the same right, and whether its
steps may read secrets is decided again by the setting at that moment.

**Save…** commits the file as you: the author is your name and e-mail address as your sign-in
provider gave them (an address the provider says is unverified is not used; without one it is
`<you>@users.noreply.launchway`), the committer is Launchway, and only the pipeline file changes.
By default it goes to a new branch, `<branch_prefix><you>/<date-time>`, with a link to open a pull
request when the repository is on GitHub, GitLab, Gitea or Forgejo (judged by its host name;
Launchway calls no Git host API). A save is refused, and nothing is pushed, if the branch has
moved on since the text was opened, so two people cannot overwrite each other; or if the text has
problems, unless an admin of the project chooses to save it anyway. Saving needs the operator role
and a `push` section for the project:

```yaml
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    push:
      branch_prefix: launchway/          # the default
      allow_direct: ["release/*"]        # branches that may be committed to directly; default none
```

Pushing uses the server's Git credentials, exactly as fetching does: in the repository URL, or in
Git settings in the server's environment, such as `GIT_CONFIG_COUNT=1`,
`GIT_CONFIG_KEY_0=http.https://git.example.com/.extraHeader` and
`GIT_CONFIG_VALUE_0=Authorization: Bearer ...`. Give that credential only the rights it needs,
and protect the branches that matter on the Git host: Launchway refuses direct saves outside
`allow_direct`, but the credential itself could push anywhere it is allowed to. A project without
`push` cannot be saved to; the editor offers *Copy* and *Download* instead. Each save works in a
temporary clone of the server's mirror, which is removed whatever happens, and pushes with a
lease, so saves on several servers at once are safe. If the push fails, Git's own message is
shown, with any credentials in it removed.

Over the API: `GET /api/v1/projects/{name}/pipeline?ref=main` reads the file;
`POST /api/v1/projects/{name}/draft-runs` with `{"ref": "main", "pipeline": "..."}` starts a draft
run (problems answer `422` with their lines); `POST /api/v1/projects/{name}/pipeline` with
`{"base_ref", "base_sha", "pipeline", "message", "branch", "force"}` saves (`branch` empty for a
new branch, or `base_ref` itself to commit to it directly; a stale `base_sha` answers `409`). The
text may be at most 256 KiB.
