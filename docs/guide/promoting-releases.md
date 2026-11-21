# Promoting a release

A release is what gets promoted: the same commit and the same image digest, moved from one Airfield to the next. Nothing is built again. This page covers the matrix that shows where each release is, promotion itself, returning an Airfield to base, formations, and what a pipeline must declare to be promotable. How to make a release is in [Making a release](releases.md); what must be true before a promotion is in [Release checklists, windows and freezes](release-checklists.md).

## Where each release is

Deployments, then Releases, then *Where each release is* (or `launchway release matrix`) is a table with a row for each project and a column for each Airfield. Each cell says what runs there: the version (a link to the release when it is one), since when and by whom, the Sortie that launched it, its health (healthy, failed, or returned to base), and any launch in progress. A version that is not a release of the project is marked as such: the matrix reads what the pipeline launched, and a deployment is matched to a release by the Sortie that a promotion started, or else by its version or tag.

## Promoting

A promotion starts the project's own pipeline as a Sortie at the release's **tag**, with these run inputs, for each the pipeline declares:

| Input | Value |
|---|---|
| `release` and `version` | the release's version |
| `airfield` | the Airfield to launch into |
| `sha` | the released commit |
| `digest` | the release's first image digest |
| `digests` | every image digest, separated by commas |

The pipeline uses them so that it deploys what was built rather than building again:

```yaml
pipeline: shop
inputs:
  airfield: { type: string, default: dev }
  release: { type: string, default: none }
  digest: { type: string, default: none }
steps:
  deploy:
    kind: deploy
    environment: prod        # or one deploy step for each Airfield, chosen with when: { input: airfield, equals: prod }
    version: { input: release }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, digest: { input: digest } }
```

A pipeline that declares none of `airfield`, `release` and `version` cannot be told what to promote, so promotion is **refused with a sentence that says so**, not run as a rebuild. Before starting the Sortie Launchway also checks that the tag still points to the released commit, that the checklist of the Airfield is met, and that the Sortie really ran at the release's commit (otherwise it is canceled and refused).

In the web interface, open the release and use *Check and promote to AIRFIELD*, or use the matrix. From the command line: `launchway release promote PROJECT VERSION --airfield prod`. The dialog shows the checklist line by line first. The environment's own rules (who may deploy there, protected environments with reviewers) apply exactly as for any deployment.

A **candidate** may be promoted to Airfields that are not protected, for testing. A protected Airfield takes only a published release. A withdrawn release is never promoted.

### Planning

`launchway release plan PROJECT VERSION --airfield prod --at 2026-11-24T14:00:00Z` writes a promotion on the [calendar](release-checklists.md#the-calendar). **A plan is a reminder**: Launchway does not start it; the person promoting still does, and the checklist is judged then.

## Return to base

*Return to base* puts an Airfield back on the release it ran before, by the rollback the product already has (see [Rollback after success](rollback-after-success.md)): the same rules, the same lock, a reason that is required and recorded, and the same warning that a rollback never restores a database or configuration. `launchway release return --project shop --airfield prod --reason "checkout fails"` (or the button in the matrix) asks for it. A promotion whose Sortie was returned is shown as returned, and counts as a failure in the [measures](measuring-delivery.md).

## Formations

A **Formation** groups releases of several projects that go out together, in an order you write:

```
launchway release formation create autumn-launch api@2.0.0 web@5.1.0 docs@2.0.0
launchway release formation promote autumn-launch --airfield prod
```

- **All or nothing to begin with.** Nothing starts unless every member passes the checklist of the Airfield (or an administrator overrides what may be overridden, with a reason). One blocked member blocks them all.
- **One after another, in order.** The first member is promoted; the next starts when the one before has launched. The server moves a promoting formation on every few seconds, and when it is read.
- **A report that says what happened.** If one member fails, the members after it are not started, and the formation is *partial* (some launched, then one failed) or *failed*. Each member shows launched, failed or not started. Nothing is undone automatically: return the launched members to base if they should go back.

The order is the one you give (`order_source` is `explicit`). Or let the [software graph](software-graph.md) order it: add `--order dependencies` (the box "Order by dependencies" on the Formations page, `"order": "dependencies"` in the API), and the releases are put in the graph's dependency order, what others depend on first, whatever order you wrote them in (`order_source` is `dependencies`). It needs the graph to know which repository each project builds, and refuses, saying which, when it does not; and it refuses when the releases depend on each other in a cycle, because then the order is yours to write.

```
launchway release formation create autumn-launch web@5.1.0 api@2.0.0 --order dependencies
```

You need the operator role on every project in a formation.
