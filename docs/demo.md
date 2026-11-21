# Try the demo

This page shows you how to run Launchway with demo data on your own machine in one command, and
walks through what you will see, screen by screen. It is for anyone who wants to look at Launchway
before installing it for a team.

> **Verified** on 2026-11-19 against commit f717337: `make all demo` in a fresh clone, then the server
> `make run` starts (`bin/launchway server --config examples/local.yaml`, on another port with
> `--listen`), a demo-app run that failed, was resumed, waited for approval and succeeded, and runs of
> demo-lib-app and demo-plain, with Podman on Linux; `scripts/docs-screenshots.sh` run twice from a
> fresh clone. **Not verified:** `make run` on port 8080 itself, Docker instead of Podman, macOS and
> Windows.

## A hosted demo

There is no hosted demo yet. Until there is one, run the demo on your machine as below; it takes a
few minutes and needs no account anywhere.

<!-- Hosted demo: when the site configuration's demoUrl is set, link it here. -->

## What you need

- Go 1.27.4 or newer, Node 24 or newer with npm, and Git.
- Podman or Docker. Steps run in containers: `docker.io/library/alpine:3.22`,
  `docker.io/library/alpine:3.21` and `docker.io/library/redis:7-alpine`, pulled the first time.
- Network access the first time, for the npm packages, the Go modules and those images.
- Port 8080 free on 127.0.0.1.

## Run the demo

From a clone of the repository:

```sh
make run
```

`make run` does this:

1. Builds the web UI (`npm ci` and `npm run build` in `web/`) and the binary, `bin/launchway`.
2. Creates four small Git repositories next to your clone: `../launchway-demo` (the demo app),
   `../launchway-demo-library` (a [shared library](reference/pipeline.md) of templates, tagged
   `v1` and `v2`), `../launchway-demo-lib-app` (an app that uses the library) and
   `../launchway-demo-plain` (a repository with no pipeline file). An existing one is left alone.
3. Starts the server with `examples/local.yaml`, at **http://127.0.0.1:8080**. It keeps its data in
   an SQLite database in `launchway-data/` in your clone, and runs steps on a built-in runner.
4. Watches four projects: `demo-app`, `demo-lib-app`, `demo-plain` and Launchway itself.

There is no sign-in: the server listens only on 127.0.0.1, and every page says "No sign-in". Open
the address in a browser, then try these:

- **Projects, demo-app, Start a run.** Pick `main`, keep the inputs, press *Start run*.
- **Watch it.** The steps appear as a graph and the logs stream in.
- **Resume it.** `smoke-test` fails on its first attempt on purpose. Press *Resume from smoke-test*:
  only that step and the ones after it run again.
- **Approve it.** The run then waits before `deploy-dev`. Open **Waiting for a decision**, then the run, and
  give it **Go**.
- **Run the feature branch.** Start a run of `feature/new-button`: a unit test fails and the
  security scan finds one more problem, shown as new.
- **Use the library.** Start a run of `demo-lib-app`: the library's `release` template is drawn as
  one group.

Stop the server with Ctrl+C. Everything is kept, so `make run` starts where you left off.

To use another port, build and create the repositories, then start the server yourself:

```sh
make all demo
./bin/launchway server --config examples/local.yaml --listen 127.0.0.1:8081
```

To remove the demo, delete what it made:

```sh
rm -rf launchway-data ../launchway-demo ../launchway-demo-library ../launchway-demo-lib-app ../launchway-demo-plain
```

> [!NOTE]
> A demo that runs only in containers, from the image `make image` builds, is not available yet.
> The image holds the server and Git but no shell or container runtime, so its built-in runner
> cannot run the demo's steps. To try Launchway in a cluster, see
> [Install on Kubernetes](howto/install-kubernetes.md).

## Screenshot tour

These screenshots come from a real server running the demo data of
`scripts/docs-screenshots.sh` (see [Make the screenshots again](#make-the-screenshots-again)). The
projects, people and addresses in them are made up.

A short silent recording of starting a run and watching it finish is in
[start-a-run.webm](public/images/tour/start-a-run.webm).

### Home

![Home for the user ana: three things need her, a failed demo-app run, and two runs waiting for a decision, then the changes on their way, each drawn as dots a change passes](images/tour/home-light.png)

What needs a person first, then every change on its way to your users, from commit to deployment.
Open **Home** in the top bar.

### Repositories

![The Code page listing five repositories: demo-app, docs-site, flight-api hosted by Launchway, payments-api and web-frontend, each with its default branch, its latest run and recent results](images/tour/code-light.png)

Every repository the server watches or hosts, with its latest run. Open **Code** in the top bar.

### Pipelines

![The Pipelines page: five pipelines, one stopped on its last run, each with how it starts, its latest run and how many finished runs passed](images/tour/pipelines-light.png)

One line for each pipeline: how it starts, its latest run and its recent pass rate. Open
**Pipelines** in the top bar.

### A pull request

![Pull request 1 of acme-flight/flight-api, Let travelers pick a seat: opened, checks 5 of 5, review needing one more approval, and a box saying Cannot merge yet because it needs 1 more approval and 1 conversation is not resolved, above the list of passed checks](images/tour/pull-request-light.png)

A repository hosted by Launchway has pull requests, reviews and merge rules. The page says in one
sentence why a change cannot merge yet, and lists the checks the pipeline ran. Open a repository
from **Code**, then **Pull requests**.

### Start a run

![The Start a run dialog for demo-app: a branch picker showing main and feature/new-button, and the pipeline's inputs scan (a choice) and alpine (a list of values)](images/tour/start-run-light.png)

Choose a branch or tag, read straight from the repository, and fill in the pipeline's inputs.
Press **Start a run** on a repository's page.

### A run as it happens

![A running web-frontend run: the graph of install, unit-tests, audit, e2e and bundle, the e2e step repeated for chromium, firefox and webkit, and the firefox log streaming below](images/tour/run-live-light.png)

The graph shows each step's state, a repeated step shows one box per value, and logs stream live.
Open any run from **Activity**, then click a step.

### A failed run and Resume

![A failed demo-app run: What went wrong, smoke-test failed with exit code 1, the last lines it wrote, and buttons to resume from the failed step or re-run from smoke-test](images/tour/run-failed-light.png)

A failed run says which step failed, why, and the last lines it wrote. **Resume this run from the
failed step** runs only that step and the ones after it, at the same commit.

### Test results of a run

![The Tests tab of a failed web-frontend run: 22 tests, 19 passed, 2 failed, 1 skipped, marked 2 new failures and 1 flaky, with the first failure's details](images/tour/run-tests-light.png)

Every JUnit or `go test -json` report the steps wrote, with new failures and flaky tests marked.
Open a run's **Tests** tab.

### Test history of a project

![Tests of web-frontend: results run by run as stacked bars, the time each run's tests took, and lists of the tests failing most, flaky tests and the slowest tests](images/tour/project-tests-light.png)

How a branch's results move over its recent runs, and which tests fail most, are flaky or slow.
Press **Tests** on a project.

### Security findings

![The Security tab of a demo-app run: 6 findings, 3 high, 2 medium and 1 low, from three scanners, listed with their rule, file and line](images/tour/run-security-light.png)

Findings from any scanner that writes SARIF, by severity, compared with the branch's earlier runs.
Open a run's **Security** tab.

### Artifacts

![The Artifacts tab of a demo-app run: an SBOM, a report from each compat step and the build's binary, each with its size and how long it is kept](images/tour/run-artifacts-light.png)

Files the steps kept, by step, with their size and how long they are kept, ready to download.
Open a run's **Artifacts** tab.

### A shared library template

![A payments-api run: the steps of the platform library's release template framed as one group named release, and the run's header naming the library release platform@v2 and its commit](images/tour/library-run-light.png)

Steps that come from a library template are framed as one group, and the run records the
library's commit. Open a payments-api run from **Runs**.

### The library

![The Templates page of the Library: the platform-library at v1 with its ready-made pipelines check, release, service-cd-rollout, service-cd, service-ci-postgres and service-ci, each with how many inputs it takes](images/tour/library-light.png)

Ready-made parts of a pipeline kept in Git, each pinned by version, with typed inputs. Open
**Library**, then **Templates**.

### Learn

![The Learn hub: a search box, Start here and The concepts, and four paths to choose from: new to Launchway, writing pipelines, deploying and operating, and administering](images/tour/learn-light.png)

The documentation, inside the product and matching the release you run. Open **Learn** in the top
bar.

### Approvals

![The Approvals page with two runs waiting: demo-app asking Deploy this build to dev? and payments-api asking Deploy this build?, each with the time left](images/tour/approvals-light.png)

Every run waiting for a person, with the question and how long is left. Open **Approvals**, then
a row, to approve or reject in the run.

### The pipeline editor

![The pipeline editor for demo-app with an unsaved change: the YAML on the left and, on the right, Valid with 10 steps in 6 stages and the graph it makes](images/tour/editor-light.png)

Edit `.launchway/pipeline.yaml` from a branch, see the graph as you type, then **Run this draft** or
**Save…**. Press **Edit the pipeline** on a pipeline's page.

### Problems in the editor

![The pipeline editor with three problems listed by line, such as needs unit-test but there is no step with that name, and line 41 highlighted in the text](images/tour/editor-problems-light.png)

Mistakes are listed with their line and checked by the same code the server uses. Click a line
number to jump to it.

### Playground

![The Playground: a small pipeline in the editor on the left and, on the right, Valid with 5 steps in 4 stages and its graph](images/tour/playground-light.png)

Write any pipeline and see whether it is valid and the graph it becomes; nothing runs.
Open a pipeline and press **Try it without saving**, or **Pipelines**, then **Try a pipeline without saving**.

### Components

![The Components page searching for left-pad: 2 projects use it, web-frontend at 1.1.3 and demo-app at 1.3.0, with every run whose SBOM lists it](images/tour/components-light.png)

Find which projects and runs use a component, from the SBOMs the runs made.
Open **Images**, then **Components**.

### Policy

![The Policy page: two policies in report mode from the server's configuration, pinned-libraries and image-digests, their rules, recent verdicts of runs, and the waivers section](images/tour/policy-light.png)

The policies in force, where they come from, and their recent verdicts; the demo's are in report
mode, so they warn but never block. Open **Settings**, then **Rules**.

### Audit trail

![the Logbook: one row per change, such as run.start, run.draft and approval.approve, with the time, who, their address, the project and run, the outcome and a detail](images/tour/audit-light.png)

Every change made through the API, who made it and whether it was allowed. Admins open
**Settings**, then **Audit trail**.

## Make the screenshots again

`scripts/docs-screenshots.sh` makes every picture on this page, and the ones in
`docs/public/images/landing/` that the README and the landing page show, from a fresh server:

```sh
scripts/docs-screenshots.sh
```

It runs `make all`, creates the demo repositories and a configuration in a temporary directory
(`scripts/docs/seed-demo.sh`), starts a server and a runner named `build-1` on 127.0.0.1:18160,
starts runs through the API, hosts a repository with a pull request, takes the screenshots with
Playwright at 1440x900 and device scale 2 in light mode (`--dark` adds dark mode), and hides the
server's path on this machine in what the pages show, makes them smaller, and stops the server and the runner. It needs what `make run` needs, plus
Python 3 with Pillow, and a Chromium for Playwright in `~/.cache/ms-playwright` (or set
`LAUNCHWAY_DOCS_CHROME`). With ffmpeg installed it also makes the recording. Set
`LAUNCHWAY_DOCS_PORT` to use another port, pass `--no-build` to use the binary you have, and
`--keep` to keep the temporary directory and its logs.

## What next

- [Write your first pipeline](tutorials/first-pipeline.md).
- [Install Launchway on your laptop](howto/install-laptop.md) or [on one server](howto/install-single-server.md).
- Read the [concepts](start/concepts.md) behind runs, steps and approvals.
