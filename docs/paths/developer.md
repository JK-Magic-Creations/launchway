# Path for developers

A reading order for people who write code and pipelines. About two hours from nothing to a
pipeline with tests, scans and a deployment.

1. [Concepts](../start/concepts.md): the ten words you need. 10 minutes.
2. [First pipeline in 10 minutes](../tutorials/first-pipeline.md): write, check and run a pipeline.
3. [Tests and reports](../tutorials/tests-and-reports.md): failures, flaky tests and history on the run.
4. [Security scans](../tutorials/security-scans.md): findings and SBOMs, new and fixed.
5. [Templates from a library](../tutorials/library-templates.md): reuse what your platform team publishes.
6. [Pull-request runs](../tutorials/pull-requests.md): check changes before they are merged.
7. [Build and sign an image](../tutorials/build-and-sign-image.md), then
   [Deploy with approval and rollback](../tutorials/deploy-with-approval.md).

Keep at hand:

- [Pipeline reference](../reference/pipeline.md): every key.
- [Environment variables](../reference/environment-variables.md): what every step receives.
- [Pipelines guide](../guide/pipelines.md): containers, services, inputs, loops, cleanup steps.
- [Command line](../reference/cli.md): `launchway validate`, `run`, `runs start --wait`.
- [Troubleshooting](../howto/troubleshooting.md).

Habits worth having: run `launchway validate` before you push; keep secrets in `secrets:`, never
in `env` or inputs; capture a test command's exit status before writing reports; pin images and
libraries where it matters. See [Hardening for pipeline authors](../security/hardening.md#for-pipeline-authors).
