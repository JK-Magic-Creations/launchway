# Release checklists, windows and freezes

Each Airfield can ask a release to pass a **checklist** before it is promoted there, and can be closed for a while by a window or a freeze. All of it is set by administrators of the project (a freeze of every project by an administrator of every project), judged by the server when someone promotes, and shown line by line so that nobody has to guess why a promotion is refused.

## The checklist

Set it with `launchway release gate set PROJECT --airfield prod ...`, from the project's release settings in the web interface, or `set_release_checklist` for an agent. An Airfield with no checklist asks only that the release is published (or a candidate, where the Airfield is not protected).

| Line | What it asks |
|---|---|
| Ready | The release is published, or a candidate going somewhere that is not protected. A draft or a withdrawn release is never promoted. |
| Notes (`--notes`) | The release has notes. |
| Checks (`--checks`) | Every pipeline that ran at the release's commit succeeded in its latest Sortie (run), and at least one ran. |
| No blocking findings (`--no-findings`) | The release's Sortie has no open critical or high findings. |
| In the previous Airfield (`--previous staging`) | The release is what runs in that Airfield and is healthy there. This also puts the Airfields in order for the matrix. |
| Soak (`--soak 120`) | It has been there at least that many minutes. |
| Clearance (`--clearances 2 --clearance-role operator`) | That many people with the role gave their Clearance for this release and Airfield. The person who published the release may not. |
| Change window | Now is inside the allowed days and hours (below). |
| No freeze | No freeze covers the project and Airfield. |

Each line is *met*, *not met* or *waiting*. Clearance is given with `launchway release clearance PROJECT VERSION --airfield prod`, or in the promote dialog.

### Overrides

A line about a window, a soak time or a freeze may be **overridden by an administrator of the project, with a reason**: `launchway release promote ... --override "customer outage"`. Everything else cannot be overridden: a missing note, failing checks, a blocking finding, a release that is not in the previous Airfield, a missing Clearance, a draft. The override and its reason are kept with the promotion, shown on the release and the report, and recorded in the Logbook (audit action `release.promote`). An operator who tries is told only an administrator may.

## Change windows

A window is the days of the week and the hours, in a time zone, in which promotions to the Airfield are allowed: `--window-days mon,tue,wed,thu --window-from 09:00 --window-to 16:00 --window-zone America/Chicago`. The end is exclusive, a window cannot pass midnight (write two rules' worth of hours instead), and outside it the line is *waiting* and says when the window opens next.

## Freezes

A freeze is a time when nothing is promoted, to one Airfield or all, for one project or all (the default word is *Hold*, the plain word *freeze*; both are the same thing). Start one with `launchway release freeze create --to 2026-12-02 --reason "quarter close" [--project P] [--airfield A]`. A freeze of one project needs the admin role on it; a freeze of every project needs the admin role everywhere. End one early with `launchway release freeze cancel ID`; it stays in the record. When a freeze begins, the channels of a notification rule that names `freeze.started` are told.

## The calendar

Deployments, then Releases, then *Calendar* (or `launchway release calendar`) lists, from two weeks back to a month ahead, what is **planned**, **in progress** and **done**, and the freezes. A promotion appears when it starts and stays as done; planned ones are the reminders written with `release plan`. The calendar shows only projects the caller may read.

## Who may do what

| Action | Role needed |
|---|---|
| See releases, the matrix, the calendar, the report | viewer of the project (drafts: operator) |
| Make, edit, publish, withdraw a release; attach files | operator |
| Promote, plan, return to base | operator on the project, and on the Airfield as the Airfield (environment) says |
| Give Clearance | the roles the checklist names, never the person who published |
| Release settings, checklists, a freeze of the project, override a window, soak time or freeze, make a release public | admin of the project |
| A freeze of every project, allow public downloads | admin of every project |
