# What the home page shows, and why

Home answers one question: what should I look at? It shows what needs you, what is running now, your projects by category, and what finished lately. It does not show a list of everything that ever failed.

## Needs you

A failure is listed only when it still needs someone. The rule is the same on Home, in the count on the Home tab, in the Sorties list (**Needs attention**), in `launchway attention` and `launchway runs list --needs-attention`, and in the MCP tool `attention`.

1. Sorties of one pipeline are grouped by where they came from: the pull request, or else the branch.
2. In each group the newest Sortie that was not canceled decides.
3. If it is queued or running, an older failure there is **superseded**: it is not listed. It shows under **Running now** as "Retrying #412". Someone is already on it.
4. If it succeeded, the group is fine.
5. If it failed, the failure is listed when the group matters to you: the project's default branch, a `release/` or `hotfix/` branch, your own pull request or a Sortie you started, or a pipeline you follow (a favorite). A failure on someone else's feature branch is not yours to chase.

Besides failures, **Needs you** lists a deployment that failed and was not followed by a successful one or by a return to the last good version, a decision that waits for someone, a Sortie queued for more than 15 minutes, and a Sortie running for more than three times its pipeline's usual time (never less than 30 minutes) that is not paused for a decision. Each entry says **why it is here** and **what to do**.

You can **Set aside** an entry or **Snooze a day** for it. That is a personal preference: it changes nothing for anyone else, it returns by itself if something newer replaces it, and **Show set aside** brings it back. Setting an entry aside is written to the Logbook as a personal preference.

## Running now

Cards for the Sorties in progress: the pipeline, the branch or pull request, the current step, how long, and who started it. Yours come first, then your Squadron's, then the rest you may see. The page asks again every ten seconds.

## By category

Your projects grouped by Squadron, each as a small card with the latest result on its default branch, what runs, and how many entries need you. Only a Squadron with something that needs a look opens by itself; the others show one line of health (how many need a look, how many run) until you open them, and each shows its six most urgent projects with a link to all of them. **Show all in the explorer** goes to the [explorer](organizing.md). Favorites come first.

## Recently finished

A small strip of what finished lately, passed and failed alike, quiet on purpose.

## Pick up where you left off

A panel of what you started or opened lately: a Sortie you started, with what changed since you were there ("Sortie #9 has finished: it failed."), a pipeline or pull request you were in, with the exact view you had (the address keeps the tab, the filter, the step). Below it, **Recent history** is searchable and grouped by day.

The trail is yours alone. It is kept on the server so it follows you across devices, but no one else can read it, administrators included, and it holds only what you could open: an entry for a project you can no longer see is hidden. It keeps 90 days (an administrator may set `retention_days` in the shared layout page `activity-settings`), **Pause history** stops it being written, **Clear my history** deletes it, and `launchway recent` and the MCP tool `recent` show it to you, and only to you.

## Panels

Home is a grid of panels in three columns. Besides the sections above you can add a **saved view** (a link to a filtered list, also made with **Pin to Home** on any explorer), a **project** status, and **notes and links** written in Markdown (only safe formatting is shown: links open in a new tab, and no raw HTML). **Arrange** adds, removes, resizes, hides and reorders panels, by drag or with the Up and Down buttons. **Arrange for me** changes only your Home; someone who may arrange at the Base also sees **Arrange for everyone**, which sets the default for everyone who has not arranged their own. A panel that a newer version made and this one does not know is shown as unavailable, never as an error.

## My work or everything

Choose **My work** (your favorites and projects you have run) or **Everything I can see**; the choice is remembered. **Arrange for me** reorders and hides sections for you only. A new installation sees its next step first, then nothing to show until the first project exists.

## Speed

Home makes one call for all of this, answered by the server from the last 14 days of Sorties (at most 30,000 of them) and cached for a few seconds. With 5,000 pipelines and 200,000 Sorties the rule takes about 120 ms on the server.
