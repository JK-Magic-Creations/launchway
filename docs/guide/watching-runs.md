# Watching a Sortie (run) live {#watching-a-run-live}

A Sortie page shows what is happening as it happens. Nothing needs a refresh: output, step states, test counts and the time estimates arrive on one live stream.

## Output within a second

A step's output reaches the page about a second after the step prints it. On the build machine the Station (runner) sends what a step has printed every 150 milliseconds, or at once when a lot has piled up; the server stores it and pushes it to every page watching, on whichever server the page is connected to.

- **Text without a newline** (a progress bar, a prompt) is shown after a quarter of a second of quiet. The rest of that line, when it comes, is a line of its own. The end of such text is held back while it could be the start of a secret, so a secret is never cut in two and never shown.
- **Image builds** print every build step the moment the builder reports it, with its timing:

  ```text
  #7 [build 3/9] RUN go build ./...
  #7 4.213 go: downloading …
  #7 DONE 41.2s
  #4 [build 1/9] FROM golang:1.26
  #4 CACHED
  ```

  Layer pushes show one summary line a second, never a line per update.
- **Tools that buffer when they are not on a terminal.** A step's output is not a terminal, so some tools (Python is the common one) hold their output back in a block buffer. Launchway sets `PYTHONUNBUFFERED=1` for every step. For others, ask the tool to flush (`stdbuf -oL tool`, `--line-buffered`, `-u`).
- **A chatty step** (hundreds of thousands of lines) is never slowed to match the page. If the server cannot keep up the step waits briefly at its output and then goes on; the page keeps the newest 20,000 lines and says how many earlier lines are not shown live. They are all in the full log, which the API, `launchway runs logs` and the MCP server serve.

## The log view

The view stays at the end of the output while a step runs. Scroll up and it stops following, says *paused while you read* and offers **Jump to latest**. While a step runs it says *live* and *last output 12s ago*; after ten quiet seconds it says *live, but quiet*, so a silent step looks silent, not frozen.

For screen readers the output is not announced line by line: a short status ("Live. 1,204 lines so far.") is updated every ten seconds.

Long logs draw only the newest 1,500 lines; **Show earlier lines** draws more.

If the live connection drops, the page fetches the lines it missed from where its last line ended (by sequence number) when it comes back.

## Following from a terminal or an assistant

```sh
launchway runs logs RUN_ID --follow           # every step, as it prints
launchway runs logs RUN_ID build --follow     # one step
```

The `get_step_log` MCP tool takes a cursor: call it with `mode: head` and `after_seq` set to the `next_seq` of the previous answer, and `wait_seconds` (up to 25) to wait for new lines.

## Time estimates

The page says how long things usually take, from earlier Sorties of the same pipeline in the same project. Nothing is stored for it: it is read from the step records that are already kept.

**Per step.** A step needs three earlier successful runs to have an estimate. The *typical* time is the median, with a range from the 20th to the 80th percentile. Recent Sorties count more than old ones, runs of the same kind (pull requests against everything else) are preferred when there are enough of them, and one freak run (a stuck Station) is ignored. A running step shows *1m 12s of about 2 min* with a small bar that is only a hint: it never gets more precise than the history.

**Two groups of times.** A step that sometimes finds its cache and sometimes does not has two groups of times. Launchway notices, shows the more common one, and says so: *about 2 min (9 min when the faster case does not apply)*.

**Past the usual.** After the 90th percentile the step shows a calm note, *taking longer than usual (usually ~2 min, now 5 min)*, with a link to compare with the step's last good run. It is not a failure and nothing is stopped.

**The whole Sortie.** The top of the page says *about 6 min left*, from the longest path through the steps still to run, each at its typical time. Steps that run side by side count once. Steps that wait for a person (Clearance) are left out and named, *waiting for Clearance*, and the time left is what remains after the decision. Queue time is shown separately, with its reason, and is not counted either. If a step has no history the estimate says *at least*, and names it. A run with no history at all says *No estimate yet*. When the run is over the page says *took 8m 03s, 12% faster than usual*.

**What it does not know.** It assumes every ready step gets a Station at once. It cannot know that a change is bigger than usual. A renamed step starts with no history; each combination of a matrix is its own step with its own history.

**For scripts.** `estimate` on `GET /api/v1/runs/{id}` (and `runs show`, and `get_run`) holds the numbers: `remaining_ms` as of `as_of`, the range, `unknown_steps`, `waiting_for_person`, `queued`, and, after the Sortie, `took_ms`, `usual_ms` and `diff_percent`. Each step has `estimate` too. In a list of Sorties, the running ones carry `estimate` as well. It is computed on the server and reused for twenty seconds, or until a run of the pipeline ends.
