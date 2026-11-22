# Releasing

A release is not cut unless the upgrade gate is green. Installing and upgrading Launchway is meant to
be boring: change the version, nothing else, by any common method, and a Return to base (rollback) works. The gate
proves it on every release instead of trusting that nobody broke it.

## The gate

```sh
make test-upgrade       # the full matrix: a throwaway kind cluster, minutes (about 40 on a laptop)
make test-upgrade-fast  # the hermetic subset: renders only, no cluster; part of make check
```

`make test-upgrade` runs `deploy/test/upgrade/matrix.sh`. It builds the previous release's image and
chart from its commit (`OLD_COMMIT`, now `093664b`; the plan is "the last N tags") and this commit's,
makes a kind cluster named `launchway-upg-1` with its own kubeconfig file (it refuses to run a cluster
command in any other context), runs each cell as an automated scenario with assertions, and ends with a
plain PASS or FAIL table. The command fails unless every cell that ran passed. A cell that could not run
here says "not run" and why; it is never shown as green.

| Cell | What it proves |
|---|---|
| install | a fresh install (the installer's Kubernetes mode, and a plain `helm install` with defaults) signs in, runs a pipeline, is encrypted, and `launchway doctor` is clean |
| upgrade, by `helm upgrade`, GitOps (single application and app-of-apps), image first, chart first | no crash loop; a project, a Sortie (run), a variable, a webhook with its secret, the SSH host keys and a pull request survive; no secret is readable in a database dump once sealing completes; one root key; `launchway doctor` is clean; and nothing but the version was changed |
| Return to base, before and after sealing | before: no manual step. After: the previous release says it cannot read sealed values, and the documented restore from a dump works end to end |
| values | the previous release's values file renders under this chart; a values file with keys only a newer chart knows renders, and says which keys were ignored |
| network | a namespace with policies already in place gets the chart's off by default (the installer says why); one without gets the chart's protecting by default; step pods cannot reach the cluster API or a public address unless opted in |
| pruning off | after a feature is turned off, the deploy step names the objects left behind |

## Before a release

1. `make check`, `make docs`, `make chart-lint`, `scripts/security/iac.sh`.
2. `make test-upgrade`. Read the table: every row PASS, or an honest "not run" that you accept and
   write into the release notes.
3. `make test-like-ci` once, at the end.
4. Raise `launchway.io/chart-revision` (and `config.ChartRevision`) whenever the server starts to rely on
   something new in the chart, `Chart.yaml` `launchway.io/bootstrap-since` when a new binary feature needs
   a chart-made object, and `launchway.io/supported-app-versions` to the app versions the chart renders
   correctly. [Upgrading](../guide/upgrading.md) explains the compatibility rule these three encode.
5. Move `OLD_COMMIT` to the previous release's commit.

A cell that fails is a bug in the release, not in the test. Do not edit the cell to pass.
