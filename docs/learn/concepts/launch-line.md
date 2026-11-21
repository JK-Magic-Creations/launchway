# The launch line

Every change you make travels from your commit to software that people use. Launchway draws that
journey as one line of **stations**, so that you never have to guess where a change is, or what it is
waiting for.

<figure class="lx-diagram lx-inline">
<svg viewBox="0 0 660 120" role="img" aria-label="A launch line of seven stations: Commit, Checks and Tests passed, Package running, Approval waiting for a person, then Staging and Verified not reached yet" style="min-width: 520px; max-width: 660px; width: 100%; height: auto; font: 13px ui-sans-serif, system-ui, sans-serif">
<line x1="40" y1="46" x2="214" y2="46" stroke="#7d8593" stroke-width="4" stroke-linecap="round"/>
<line x1="214" y1="46" x2="300" y2="46" stroke="#5b7cff" stroke-width="4"/>
<line x1="320" y1="46" x2="580" y2="46" stroke="#7d8593" stroke-width="3" stroke-dasharray="2 7" stroke-linecap="round"/>
<g fill="none" stroke="#7d8593" stroke-width="3"><circle cx="40" cy="46" r="13"/><circle cx="127" cy="46" r="13"/><circle cx="214" cy="46" r="13"/></g>
<g fill="#7d8593"><circle cx="40" cy="46" r="13"/><circle cx="127" cy="46" r="13"/><circle cx="214" cy="46" r="13"/></g>
<g fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M34 46l4 4 8-9"/><path d="M121 46l4 4 8-9"/><path d="M208 46l4 4 8-9"/></g>
<circle cx="301" cy="46" r="13" fill="none" stroke="#5b7cff" stroke-width="4"/><circle cx="301" cy="46" r="5" fill="#5b7cff"/>
<circle cx="388" cy="46" r="13" fill="#d99a2b"/><g stroke="#fff" stroke-width="3" stroke-linecap="round"><path d="M384 40v12M392 40v12"/></g>
<g fill="none" stroke="#7d8593" stroke-width="2.4" stroke-dasharray="3 4"><circle cx="475" cy="46" r="13"/><circle cx="562" cy="46" r="13"/></g>
<g fill="currentColor" text-anchor="middle"><text x="40" y="86">Commit</text><text x="127" y="86">Checks</text><text x="214" y="86">Tests</text><text x="301" y="86">Package</text><text x="388" y="86">Approval</text><text x="475" y="86">Staging</text><text x="562" y="86">Verified</text></g>
<g fill="currentColor" text-anchor="middle" font-size="11" opacity=".65"><text x="127" y="104">passed</text><text x="301" y="104">running</text><text x="388" y="104">waiting for a person</text><text x="475" y="104">not yet</text></g>
</svg>
</figure>

## The stations

A line is made from the pipeline's own steps, so it shows what *your* pipeline does. Launchway
groups the steps into stations it can name:

| Station | What happens there |
|---|---|
| **Commit** | The change itself. It is always passed: it is where the line begins. |
| **Checks** | Steps that look at the code, such as linting. |
| **Tests** | Steps that run tests and report them. |
| **Package** | Steps that build something to ship, such as a container image. |
| **Approval** | A step where a person says go or no-go. |
| **An environment** | A deployment to a place such as `staging` or `prod`. Each environment is its own station, named after it. |
| **Release** | Steps that come after a gate, such as tagging a release. |
| **Verified** | Launchway checks that what it deployed is healthy. |

A pipeline without a deploy has no environment stations. The line only shows what applies.

A station can stand for several steps. When it does, it shows a small stack with their number, and says how they are
doing, such as "3 at once: 2 passed, 1 running"; hover over it to see each step. The full shape, with the steps that run
together side by side, is the graph on the run page.

## Reading the colors

The color of the track carries the state, so you can read a line at a glance:

- slate: passed
- blue: moving, a step is running
- amber: waiting for a person
- red: stopped, and the station says where
- dotted: not reached yet

Only the last station turns green, when the change has arrived and is healthy.

## Where you see it

- On [Home](app:/), under "changes on their way", one line for each change in flight.
- On every run page, where each station opens its steps and their logs.
- In the list of [runs](app:/activity), as a small line beside each run.

## When a change waits

A change that stops is waiting for something, and Launchway always says what: a person to decide, a
runner to pick the work up, another step to finish, an environment's lock (so two deployments never
run at once), or a timer before a retry. The sentence is on the run page, under the station.

## Next

- [Projects, pipelines, steps and runs](pipelines-and-runs.md): what the line is made from.
- [Approvals](approvals.md): the stations where a person decides.
