# Tutorials

Lessons that take you from nothing to a working result, one feature at a time. Each one was run
end to end against the commit it names, and says what it needs and how long it takes.

| Tutorial | You will | Needs |
|---|---|---|
| [First pipeline in 10 minutes](first-pipeline.md) | write, check and run a pipeline, then let a server run it on each commit | a laptop with Podman or Docker |
| [Tests and reports](tests-and-reports.md) | show test results, failures and flaky tests on a Sortie (run) | the same |
| [Inspections (security scans)](security-scans.md) | show scanner findings and an SBOM, new and fixed across runs | the same |
| [Build and sign an image](build-and-sign-image.md) | build, scan, push and sign a container image, then verify it | Podman, a local Depot (registry), BuildKit |
| [Deploy with approval and Return to base (rollback)](deploy-with-approval.md) | deploy through Argo CD with a Pathfinder (canary), an approval, promote and Return to base | a kind cluster with Argo CD and Argo Rollouts |
| [Policy gates and waivers](policy-gates.md) | stop a deployment that lacks evidence, then waive a rule | a laptop |
| [Pull-request runs](pull-requests.md) | run the pipeline for pull requests, without secrets | a laptop |
| [Templates from a library](library-templates.md) | share steps between repositories with a versioned library | a laptop |

New to Launchway? Read [Concepts](../start/concepts.md) first.
