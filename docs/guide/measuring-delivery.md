# Measuring delivery

The release report works out the four measures of delivery from what Launchway already records, so there is nothing to instrument. Deployments, then Releases, then *Report* (or `launchway release report`, or `get_release_report`) shows them for a Flight (codebase) or for every Flight you may read, over a period.

| Measure | How it is worked out |
|---|---|
| **Frequency** | Successful launches a week. A launch is a delivery that reached an Airfield; a return to base is not counted as a launch of its own. |
| **Lead time** | The median time from the release's commit to the launch reaching an Airfield, over the launches whose release has a commit time (the count is shown). A launch that is not a release of the Flight has none. |
| **Change failure rate** | Launches that failed, or were returned to base, divided by all launches in the period. |
| **Time to restore** | The median time from a failed launch to the next successful one in the same place (a return to base is the successful one that restores it); how many are not restored yet is shown. |

Each measure is shown overall, per Airfield and per Flight, and launches over time are a chart with the table of the same numbers under it. Nothing is guessed: a period with no launches is zero, and a measure with no data says "none". The report also lists what shipped in the period: each published release, when it reached each Airfield, the lead time to each, and how many failures and returns it had.

**Export.** *Export as HTML* (or `launchway release report --html report.html`) writes one self-contained page with no script and nothing loaded from outside, light and dark by the reader's setting, with the same tables. The JSON is the API's answer (`GET /api/v1/release-report`).

**What it does not know.** A deployment Launchway never saw (a change made by hand in the cluster) is not a launch. Lead time starts at the commit's time, not at the merge, because that is what Git records; with squash merges they are close. The measures say nothing about quality by themselves, and they are not a way to rank people.

Notifications for release events are in [Notifications](notifications.md): `release.published`, `release.promoted`, `release.returned` and `freeze.started`.
