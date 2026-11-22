# Proposals from analysis

Analysis tools find a lot: a scan reports forty vulnerable libraries, a quality check reports two hundred
unused functions. Filing one item for each would bury the board. In Launchway an analysis does not file
items. It sends **proposals**, and the server groups them, caps them and holds them for a person to
review. This page says how, so you know what to expect when a source starts sending.

The same rules apply in the web interface, over the HTTP API, with the MCP tools and from the command
line. The shape of the data is in the [work format reference](../reference/work-format.md#proposals).

## The rule in one paragraph

A **source** (a name such as `security-scan`) sends a **batch** of findings. Findings that share a
**group key** become one item with a checklist, one line for each occurrence. At most ten new items are
opened in one Sortie (run); the rest fold into one summary item that says "and N more". Nothing becomes an item
until a person accepts the group, unless the level lets that source file without a review. A finding a
person dismissed is remembered and never proposed again. Sending the same batch again changes nothing
that is already in line.

## Grouping

Every finding has two keys:

- a **dedupe key**, stable across runs: the same finding has the same key every time (a package and a
  CVE number, or a file and a rule);
- a **group key**, one for each kind of problem: `vulnerable-dependencies`, `containers-run-as-root`.

Forty vulnerable libraries with one group key are one item, "Update 40 dependencies with known
vulnerabilities", with forty lines on its checklist. A finding with no group key is its own group. The
group title may contain `{n}`, which stands for the occurrences still open.

## The cap and "and N more"

One source opens at most **10 new items** in one Flight (codebase) in one Sortie (`proposals.max_items` in the
[settings](../reference/work-format.md#settings) changes it). The groups beyond the cap are not lost: they
fold into one summary item that lists them, marked as folded in the review list, and you can accept any of
them from there.

## Review by default

After a batch arrives, each group waits in the review list:

```
launchway work proposals list --repo acme/shop --state pending
launchway work proposals accept --repo acme/shop --source security-scan vulnerable-dependencies
launchway work proposals accept --repo acme/shop --source security-scan root-containers \
    --title-for root-containers="Run containers as a non-root user"
launchway work proposals dismiss --repo acme/shop --source security-scan typo-in-readme --reason "not worth an item"
launchway work proposals merge --repo acme/shop --source security-scan lint-a lint-b --into lint-a
```

Accepting opens the item of the group, with its checklist. You may change its title, description or
priority as you accept. Dismissing needs a reason. Merging joins groups into one item. Only people who may
operate the Flight review, and an agent's token cannot propose or review: an analysis is run by your
tools, and a person decides what becomes work.

A level can let a source file without a review. In the settings, `proposals.auto_accept` lists the sources
and the least severity each may file on its own (0 none, 1 low, 2 medium, 3 high, 4 critical), and
`proposals.reason` says why (required). Findings below the severity still wait for a review. The cap still
applies. A nearer level's `proposals` replaces a farther one's. Setting it is for those who maintain the
level, and the change is audited.

## Dismissed findings are remembered

A dismissed finding is matched by its source and dedupe key. When a later run reports it again, it is
counted as dismissed and left out; no item is made. The reason and the person are kept with it.

## Re-running

A source may send its full result on every Sortie and say so with `complete: true`. Launchway then keeps the
items in line:

- a new finding in an accepted group is **added** to the checklist;
- a finding the Sortie no longer reports is **ticked off** as fixed;
- when every occurrence is fixed, the item **closes**, with a comment that says why;
- when a finding comes back after that, the item **opens again**, with a comment.

Without `complete`, findings missing from a batch are left as they were, which suits a source that only
looks at part of the code.

## How machine-proposed work is marked

An item made from a proposal carries the labels `machine-proposed` and `source:NAME`, its reporter is
`source:NAME`, and its history starts with a `proposed` event that names the source, the Sortie and the
report link. Search finds them: `label:machine-proposed`, `label:source:security-scan`.

## The repository health check

The first source built in is `repository-health`. It looks at a hosted repository's default branch and
proposes what is missing, as one group, "Set up the repository basics":

| Checked | Proposed when |
|---|---|
| README | no README file in the root |
| License | no LICENSE or COPYING file |
| Owners file | no CODEOWNERS or OWNERS file (also under `.github` or `docs`) |
| Pipeline file | no `.launchway/pipeline.yaml` |
| Protected default branch | no merge rule covers the default branch |
| Tests | no test files in the root or the usual source directories |

It does not yet check how many branches are stale or whether very large files sit in the history.

It runs on demand, and once after an [import](../reference/work-format.md) when the copy has its first
commits:

```
launchway work health --repo acme/shop
```

Running it twice does not duplicate: the second run updates the same group, and a basic you set up in
between is ticked off. The group waits for review like any other.

## Other sources

The [security findings](security-findings.md) and code-quality checks send to the same route, with the
same two keys. A batch looks like this:

```json
{
  "scope": "project:acme/shop",
  "source": "security-scan",
  "run_id": "2026-11-21.3",
  "report_url": "https://reports.example/acme/shop/3",
  "complete": true,
  "proposals": [
    {
      "dedupe_key": "dep:lodash-4.17.20:CVE-2021-23337",
      "group_key": "vulnerable-dependencies",
      "group_title": "Update {n} dependencies with known vulnerabilities",
      "title": "lodash 4.17.20 (CVE-2021-23337)",
      "severity": 3,
      "labels": ["security"],
      "evidence": ["https://reports.example/acme/shop/3#lodash"]
    },
    {
      "dedupe_key": "dockerfile:api/Dockerfile:root",
      "group_key": "containers-run-as-root",
      "group_title": "Run {n} containers as a non-root user",
      "title": "api/Dockerfile",
      "severity": 2
    }
  ]
}
```

Send it with `launchway work propose --from batch.json`, or `-` to read standard input. The answer counts
what was new, already known, fixed and left out as dismissed, and how many groups wait for review.
