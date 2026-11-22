# Move your pipelines over

Bring the pipelines a team already has into Launchway in a guided hour, not a rewrite. The importer reads the pipeline files
of another system, proposes a Launchway pipeline, and tells you, construct by construct, what it carried over exactly, what it
carried over with a difference, and what it could not carry over and why. It never drops anything silently and never invents
behavior: a construct it does not understand stays visible in the proposal and in the report.

The importer reads the files and nothing else. It does not run them, does not fetch anything they include from elsewhere, and
does not evaluate expressions or program code in them. It never changes your old files.

## The five formats

| Format | Where the files live | `--from` |
|---|---|---|
| Hosted workflow files | `.github/workflows/*.yml`, and the same layout in a self-hosted forge's own dot-directory | `hosted-workflows` |
| Stage-based pipeline file | a file named `.*-ci.yml` at the top of the repository | `stages-yaml` |
| Declarative Groovy pipeline file | a file with a `pipeline { ... }` block, whose name usually ends in `file` | `groovy-declarative` |
| Jobs-and-workflows config file | `config.yml` in a dot-directory at the top of the repository | `jobs-config` |
| Tasks-and-stages pipeline file | a file named `*-pipelines.yml` at the top of the repository | `tasks-pipelines` |

The last two were chosen for reach: the jobs-and-workflows format is the most widely used among teams that host their pipelines
as a service, and the tasks-and-stages format is the one most enterprises on a large vendor's suite use. They are read to the depth
of jobs, steps, images, caches and triggers. A scripted Groovy pipeline is program code: the importer says so and lists the stages it recognized
so that a person can write the steps.

## Move over in six steps

1. **Read the proposal.** In a clone of the repository:

   ```sh
   launchway import pipeline --dry
   ```

   It finds the files of every known format under the current directory and prints the proposed pipeline and the findings.
   Use `--from FORMAT` and `--file PATH` to narrow it down, and `--report report.md` to save the findings for the team.
2. **Read the findings.** Each one names the file and line it concerns, says in plain words what happened and what to do.
   Start with the ones that need a person.
3. **Save it.** `launchway import pipeline --dry --write` writes `.launchway/pipeline.yaml`. A proposal that does not pass
   `launchway validate` is not written. Old files are left exactly as they are.
4. **Fix what needs a person.** Create the secrets it names (names only: the importer never reads a value), name an image where it
   used the default, rewrite the lines marked `NEEDS A PERSON`. In the web interface the same steps are a wizard: **Import an existing pipeline**
   on a new or empty Flight, with the old and the new text side by side, and a pull request at the end.
5. **Run old and new side by side.** See below.
6. **Switch.** Turn delivery on, then retire the old files.

## Run old and new side by side

A dry import (`--dry`, or the switch in the wizard) marks every step that delivers (pushes an image, publishes a package, creates a release,
deploys, or targets an environment) as not yet enabled. The pipeline gets one input, `enable_delivery`, which is `false` by default; every
delivering step is written with `when: { input: enable_delivery }`, so it is skipped. Builds, tests, scans and reports run as they would.

Commit the pipeline next to the old files. Every push now starts a Sortie in Launchway while the old system keeps doing what it did. Compare
what each side says about the same commit: which steps passed, how long they took, which tests failed, what the reports hold. Differences are
the findings you have not dealt with yet, or a command that behaves differently in a container image from the old machine.

When the results agree, change the default of `enable_delivery` to `true` in `.launchway/pipeline.yaml` (one line), turn the old system's delivery off,
and delete the old files. To go back, set it to `false` again.

## How to read a finding

- **Converts exactly.** Launchway does the same thing. Nothing to do.
- **Converts with a difference.** It was converted, and the finding states the difference, for example that a service is reached at `localhost`
  instead of by its name, or that a cache is kept under Launchway's rules for branches and pull requests. Check each one once.
- **Needs a person.** It was not converted. The finding gives the reason and the next step. Where a step could not be converted, the proposal contains a
  line that prints `NEEDS A PERSON` and fails, so that a half-converted pipeline never passes by accident.

Secrets and variables are recorded by name only. Where a script reads a secret, the proposal asks a Station's secret store for it by that name; you create
the secret there. Where a job is plainly the standard test job of a language (setup of the toolchain and one standard test command), the proposal uses the
built-in template for it, which also records test and coverage reports; the finding says so.

## Limits and safety

The importer is safe to give files you did not write. Files are limited in size, in how deeply they nest and in how far anchors and aliases may expand;
a file over a limit is refused with a finding. It evaluates nothing. The web interface and the API do not store what you send: the files live for the length of
the request.

## What each format converts

The tables below are made from the importer's own classification of each construct, by the same code that reports the findings, so they cannot differ from what it does.


<!-- importer-tables:start (generated by internal/importer: go test ./internal/importer -run TestGuide -update-docs) -->

## Declarative Groovy pipeline file

Where it lives: a file with a pipeline { ... } block at the top (its name usually ends in file; any file name works with --from). Import it with `launchway import pipeline --from groovy-declarative`.

38 constructs are known: 13 convert exactly, 10 convert with a difference, 15 need a person.

### Declarative Groovy pipeline file: Converts exactly

| In the old file | What the importer does | What you do |
|---|---|---|
| pipeline { ... } with stages and steps | The stages became steps that run one after the other | Nothing. |
| stage with steps | The stage became a step that needs the stage before it | Nothing. |
| agent { docker 'image' } | The docker image became the step's image | Nothing. |
| environment { NAME = 'value' } | Variables carry over as the environment of the steps | Nothing. |
| parameters (string, text, booleanParam, choice) | Parameters became run inputs; steps read them as $LAUNCHWAY_INPUT_<NAME> | Nothing. |
| when { branch, tag, changeset, buildingTag, changeRequest, allOf, anyOf } | The condition became a when: filter | Nothing. |
| parallel stages | Stages in a parallel block run side by side, and the stage after them waits for all | Nothing. |
| matrix { axes { axis } } | The matrix became for_each; each axis is also an environment variable of the same name | Nothing. |
| sh and echo | Commands carry over | Nothing. |
| dir and withEnv | The directory and the variables wrap the commands inside | Nothing. |
| archiveArtifacts | The files became cargo of the step | Nothing. |
| junit | The test results became a tests report of the step | Nothing. |
| checkout scm | Launchway checks the repository out before the first step | Nothing. |

### Declarative Groovy pipeline file: Converts with a difference

| In the old file | What the importer does | What you do |
|---|---|---|
| agent any, agent none | No agent label is carried: Launchway runs each step in a container image on a Station it chooses | Name the image the steps need |
| agent { label '...' } | The agent label … is not carried: Launchway chooses the Station by its own configuration | Name the image the step needs, or use the runner key of the step to pick a Station |
| environment { NAME = credentials('id') } and withCredentials | The credential … is read by name from a secret store, as the variable …; no value was copied. If it was a user name and password, the old system also set a _USR and a _PSW variable, which are not set here | Create the secret (or one for the user name and one for the password) in the Station's store |
| options { timeout(...) } and a timeout step | The time limit applies to each step of the job, instead of to the whole pipeline or the block it wrapped | Check the limit is long enough for one step |
| options { retry(n) } and a retry step | The step is retried as a whole, not just the block it wrapped | Check that running the whole step again is safe |
| buildDiscarder, timestamps, ansiColor | The option … is not carried: log retention is the server's policy and logs are timestamped by default | Nothing |
| triggers { cron('...') } | The cron time … was written as a schedule; an H (spread the time) became 0 because the schedule needs an exact minute | Check the time, which is in UTC unless the file says otherwise |
| post { always, success, failure, cleanup } | The post section became extra steps that run after the stage (upstream: always or failed); results of junit and archiveArtifacts there became reports of the stage's own step | Check the order and what each extra step needs |
| input { message ... } and the input step | The question became an approval step; submitter and parameters are not carried | Name the approvers (users or groups) on the approval |
| ${env.NAME}, ${params.NAME}, $NAME in a double-quoted string | The expression … was replaced by the shell variable … | Check the name: Launchway sets LAUNCHWAY_* variables instead of BUILD_NUMBER and BRANCH_NAME |

### Declarative Groovy pipeline file: Needs a person

| In the old file | What the importer does | What you do |
|---|---|---|
| A scripted pipeline (node { ... }) | This file is a scripted pipeline: it is program code, which is not read or run here. The stages that could be recognized are: … | Write a step for each stage by hand, or rewrite the file in the declarative form first and import it again |
| @Library and shared library calls | The file uses a shared library (…), whose code is not in this file | Copy what the library does into steps or a template (see libraries) |
| agent { dockerfile } or kubernetes | The agent … is not carried | Build the image with an image step, or name a ready image for the step |
| disableConcurrentBuilds | Runs that must not overlap are not carried | Set supersede: true to cancel older runs, or add a lane to make runs take turns |
| Any other option | The option … is not carried | Check what it did and write it by hand |
| password and file parameters | The parameter … holds a secret or a file and is not carried | Create a secret and read it from the step's secrets |
| pollSCM, upstream and other triggers | The trigger … is not carried | Pushes start runs on their own; use a result trigger or a webhook for the rest |
| tools { maven '...', jdk '...' } | The tools … are installations named in the old server's settings | Choose an image that has them (for example a maven or JDK image) and set it on the step |
| when { expression { ... } }, not, equals, environment | The condition `…` is program code or a negation, which Launchway does not run, so the step always runs | Write the test as a when: filter, or inside the script |
| post { changed, fixed, regression, unstable, aborted } | The post condition … compares with earlier runs or the unstable state, which has no equivalent | Write the step by hand if it still matters |
| bat and powershell | Windows commands cannot run in a Linux container image | Rewrite the command for sh, or run it on a Station that runs Windows |
| script { ... } (Groovy code) | The block is Groovy code, which is not run here; its text is kept as a comment in the script | Rewrite it as shell commands |
| stash and unstash | Passing files between stages with stash is not carried | Keep the files as cargo of the first step (artifacts) and fetch them in the next |
| A step from a plug-in | The step … comes from a plug-in that Launchway does not know | Replace it with the command it runs, or ask a person to decide |
| Other Groovy expressions in strings | The expression `…` is Groovy code and was left as written, so the command will not run as it is | Replace it with a shell expression or an input |

## Hosted workflow files

Where it lives: .github/workflows/*.yml, and the same layout in a self-hosted forge's own dot-directory (.<forge>/workflows/*.yml). Import it with `launchway import pipeline --from hosted-workflows`.

60 constructs are known: 18 convert exactly, 20 convert with a difference, 22 need a person.

### Hosted workflow files: Converts exactly

| In the old file | What the importer does | What you do |
|---|---|---|
| on: push with branches, tags, paths | The push trigger carries over, with its branch, tag and path filters | Nothing. |
| on: schedule (cron) | The cron times carry over; both use five fields in UTC | Nothing. |
| on: workflow_dispatch and its inputs | Every Launchway pipeline can be run by hand; the inputs became run inputs of the same names | Nothing. |
| env at workflow, job and step level | Variables carry over; a step's own values wrap its commands, a job's values are the step's environment and the workflow's are the pipeline's defaults | Nothing. |
| A job with run steps | The job became a step; its commands run in order in one script | Nothing. |
| needs | Dependencies between jobs carry over | Nothing. |
| if: with a branch, tag or event test | The condition became a when: filter | Nothing. |
| timeout-minutes | The time limit carries over | Nothing. |
| continue-on-error on a job | The job may fail without failing the run (on_failure: continue) | Nothing. |
| container (the job's image) | The container image became the step's image | Nothing. |
| strategy.matrix with include and exclude | The matrix became for_each, with exclude and add entries | Nothing. |
| fail-fast and max-parallel | fail-fast and max-parallel carry over | Nothing. |
| run steps | Commands carry over | Nothing. |
| working-directory on a step | The directory change wraps that step's commands | Nothing. |
| actions/checkout | Launchway checks the repository out before the first step | Nothing. |
| setup-go, setup-node, setup-python, setup-java, setup-dotnet, setup-ruby | The toolchain version became the image of the step (…) | Nothing. |
| actions/upload-artifact | The files became cargo (artifacts) of the step; retention-days became retain | Nothing. |
| actions/download-artifact | The download became a fetch of the step that keeps the artifact | Nothing. |

### Hosted workflow files: Converts with a difference

| In the old file | What the importer does | What you do |
|---|---|---|
| on: pull_request | Pull requests run at the pull request's head commit, filtered by the branch they target; the event types (opened, synchronize, ...) are not carried, because a run starts when a pull request opens or its head moves | Nothing, unless you relied on the other types (labeled, closed): use a webhook trigger for those |
| A dispatch input of type environment | The input … was written as a plain text input, but it no longer lists the old system's environments | Make it a choice input listing your Airfields (environments) if the pipeline should offer them |
| branches-ignore, tags-ignore, paths-ignore on push or negated patterns | Launchway cannot exclude branches or tags by pattern, so … was not carried; path exclusions use paths_ignore, which cannot be combined with paths | List the branches or tags that should run instead of the ones that should not |
| concurrency with cancel-in-progress | Cancelling older runs became supersede: true, which cancels older runs of the same branch, not runs that share the group name | Check that cancelling by branch is what the old group name meant |
| permissions (token scopes) | Token scopes are not carried: a Launchway step holds no repository token unless it is given a secret | If a step pushes, comments or releases, give it a named secret and use it in the script |
| defaults.run (working-directory, shell) | The default working directory was applied to each command; the default shell is not carried because steps run with sh -e | Check the commands run under sh, or use an image with bash |
| runs-on (a hosted runner label) | The runner label … is not carried (this holds for every job that names it): Launchway chooses the Station by its own configuration and runs steps in a container image | Name the image the job needs; to pick a Station use the runner key of the step |
| environment (deployment target and its protection rules) | The job deploys to the environment …, so it is marked as delivering. The reviewers and wait timers set on that environment in the old system were not read | Add an approval step before it, or protect the Airfield; see the approvals guide |
| services (containers beside the job) | The service … runs beside the step, but it is reached at localhost, not by its service name | Change the host name your tests use from the service name to localhost |
| shell: bash, sh | The commands run with sh -e, not … | Check the script for bash-only syntax |
| $GITHUB_OUTPUT | The file for step outputs was replaced by $LAUNCHWAY_OUTPUTS; later steps of the same job cannot read it, only steps that need this one | Read values in other steps as $LAUNCHWAY_NEEDS_<STEP>_<NAME> |
| GITHUB_SHA, GITHUB_REF_NAME and other variables the old system sets | The variable … was replaced by its Launchway counterpart | Check the value: Launchway reports branch and tag separately |
| ${{ ... }} for matrix, inputs, secrets, env, vars and the commit | Expressions were replaced by shell variables where the value is known: … | Nothing, unless the new value reads differently |
| vars.NAME (configuration variables) | The variable … is set in the old system, not in the file; here it is read from an environment variable of the same name | Create a variable with that name for the Flight (names only; see the variables guide) |
| actions/checkout with path, ref, fetch-depth, submodules or token | The checkout options … were not carried: Launchway checks out the commit that started the run | Fetch history or submodules in the script (git fetch --unshallow, git submodule update) if needed |
| A setup action reading the version from a file or a range | The version is written into the image tag (…) instead of being read from a file at run time | Update the tag when the file changes, or keep the image tag in a pipeline variable |
| actions/cache, cache/restore, cache/save | The cache for … is kept by Launchway between runs, keyed by the files named in the key; where entries can be read follows Launchway's rules for branches and pull requests | Check the key files and paths |
| docker/build-push-action | The image build became an image step, which scans and records provenance as well; it pushes to Launchway's built-in registry, not the registry named in the old file | Check the tags, and name another registry in the step if you push elsewhere |
| docker/setup-buildx-action, docker/setup-qemu-action | Not needed: image steps build with the Station's builder, and platforms are a setting of the image step | Nothing |
| A third-party action with a known command | The step runs the third-party program …; the command … does the same job and was written in its place | Make sure the command is installed in the step's image |

### Hosted workflow files: Needs a person

| In the old file | What the importer does | What you do |
|---|---|---|
| on: pull_request_target | This trigger runs with secrets against code from a pull request. Launchway does not run untrusted pull request code with secrets | Decide what must run on a pull request from a fork; see the pull request trust guide, then add a pull_request trigger by hand |
| Other events (release, issues, workflow_run, workflow_call, merge_group, ...) | The event … starts the old workflow but has no trigger in the pipeline file | Use a webhook trigger or a result trigger (see the triggers guide), or start the pipeline by hand |
| concurrency without cancelling (runs take turns) | The old file made runs wait for each other; the pipeline file was written without a rule for that | Add a lane to the pipeline (see the pipeline reference, Lane) if runs must not overlap |
| runs-on a Windows or macOS machine | The job asks for …, and Launchway steps run in Linux container images | Run it on a Station with that operating system, or drop it from the pipeline |
| if: with any other expression | The condition `…` is an expression, and Launchway has no expression language, so the job now always runs | Express it as a when: filter (branch, tag, event, changed, input) or move the test into the script |
| Job outputs | The job passes values to later jobs through outputs: (…), which has no direct form | Write name=value lines to the file in $LAUNCHWAY_OUTPUTS and read $LAUNCHWAY_NEEDS_<STEP>_<NAME> in the later job |
| container options, volumes, ports, credentials | The container settings … were not carried | Set what you need in the step's container: env, cpu, memory; registry credentials belong in the Station's configuration |
| matrix include entries that add extra values | An include entry adds the values … to existing combinations, which Launchway cannot express | Write the extra values into the commands, or add the combination as a whole entry with every key |
| A matrix built from an expression (fromJSON) | The matrix is made at run time by an expression, which Launchway does not evaluate | Write the lists in the file, or make a list input and loop over it |
| A job that calls a reusable workflow (uses:) | The job calls the workflow …, which holds the real steps | Import that workflow file as well and copy its steps here, or write a template (see libraries) |
| shell: pwsh, python, cmd and others | The step uses the shell …, which Launchway does not run | Put the script in a file and run it with its interpreter in an image that has it |
| if: on a step | The step's condition `…` is not carried; the step always runs | Put the test in the script (if [ ... ]) or split the step into a job with a when: filter |
| continue-on-error on a step | The step may fail without failing the job in the old file; here a failing command fails the step | Append // true to the command, or move it to a step of its own that is allowed to fail |
| timeout-minutes on a step | A time limit on one step is not carried | Use the job's timeout, or the timeout command in the script |
| Workflow commands and files (GITHUB_ENV, GITHUB_PATH, ::set-output, step summaries) | The script uses …, which only the old system provides | Print messages with echo, export variables and extend PATH inside the same script, and write key=value lines to $LAUNCHWAY_OUTPUTS to pass values to later steps |
| ${{ ... }} with functions or other contexts | The expression `…` is evaluated by the old system and was left as written, so the script will not run as it is | Replace it with a shell command, an input or a variable |
| secrets.GITHUB_TOKEN | The old system made a token for each run, and Launchway does not | Create an access token for the repository and give it to the step as a named secret |
| Two setup actions in one job | The job sets up … as well as another toolchain, and a Launchway step runs in one image | Use an image that has both, or split the job in two steps |
| A cache of a directory outside the workspace | The cache path … is outside the workspace, and Launchway keeps only paths below it | Make the tool use a directory below the workspace and cache that |
| docker/login-action | A registry login for … is not copied: Launchway logs in to registries from the Station's configuration | Add the registry and its credentials to the Station's image settings (names of secrets only in the pipeline) |
| docker/metadata-action | Tags and labels worked out by this step were not carried | Write the tags on the image step: a commit tag with {run: sha}, a fixed tag, or a version from an input |
| Any other action | This step runs a third-party program (…) that Launchway does not know | Replace it with the command it runs or one of the built-in templates (launchway templates list), or ask a person to decide |

## Jobs-and-workflows config file

Where it lives: a config.yml in a dot-directory at the top of the repository. Import it with `launchway import pipeline --from jobs-config`.

30 constructs are known: 13 convert exactly, 7 convert with a difference, 10 need a person.

### Jobs-and-workflows config file: Converts exactly

| In the old file | What the importer does | What you do |
|---|---|---|
| workflows with jobs and requires | Each job of a workflow became a step; requires became needs | Nothing. |
| A job with a docker image and steps | The job became a step; its steps run in order in one script | Nothing. |
| docker (the first image is the job's image) | The first image became the step's image | Nothing. |
| executors and executor: references | The named executor was read and its image and variables written into the job | Nothing. |
| run (command, name, environment, working_directory) | Commands carry over; environment and working_directory wrap that command | Nothing. |
| checkout | Launchway checks the repository out before the first step | Nothing. |
| store_artifacts and store_test_results | The files became cargo of the step; test results became a tests report | Nothing. |
| A command defined in the file (commands:) | The command's steps were written into the job at the place it was called | Nothing. |
| environment on a job | Variables carry over as the environment of the step | Nothing. |
| filters with branches only and ignore (plain names) | The branch names became a when: filter | Nothing. |
| filters with tags | The tag names became a when: filter and a tag trigger | Nothing. |
| type: approval | The approval became an approval step | Nothing. |
| triggers with a schedule | The cron time became a schedule trigger | Nothing. |

### Jobs-and-workflows config file: Converts with a difference

| In the old file | What the importer does | What you do |
|---|---|---|
| docker (the other images are services) | The image … runs as a service beside the step, reached at localhost, not by its name | Change the host name your tests use to localhost |
| resource_class | The machine size … is not carried | Set cpu and memory on the step's container if the job needs them |
| restore_cache and save_cache | The cache for … is kept by Launchway between runs; the key's {{ checksum "file" }} became a file list, and other parts of the key were dropped | Check the key files and paths |
| persist_to_workspace and attach_workspace | Files passed to later jobs became cargo that those jobs fetch, kept under the name …, instead of a shared workspace | Check the paths the later jobs read |
| setup_remote_docker | Not needed: image steps build with the Station's builder | Replace docker build and docker push in the script with an image step |
| parallelism | The job became a loop of … members, and the old index and total variables are set as environment variables | Check how the tests are split between members |
| working_directory on a job | The job's directory … is written as a directory change for each command; the old default was a path in the home directory | Check relative paths |

### Jobs-and-workflows config file: Needs a person

| In the old file | What the importer does | What you do |
|---|---|---|
| machine, macos, windows executors | The job runs on …, and Launchway steps run in Linux container images | Choose an image, or run it on a Station of that kind |
| run with when: on_fail or always, background, no_output_timeout | The step setting … is not carried | Move the command to a step of its own with an upstream condition, or add // true and a timeout in the script |
| A cache of a directory outside the workspace | The cache path … is outside the workspace, and Launchway keeps only paths below it | Make the tool use a directory below the workspace and cache that |
| A command or job with parameters | The parameter … is filled in when the old system runs the job, and is left as written | Write the values in place, or use run inputs |
| when and unless steps | The step block with a condition is not carried; its commands are not in the script | Write the test in the script, or use a when: filter on a step of its own |
| A step from an orb (a shared package) | The step … comes from a shared package that Launchway does not read | Replace it with the commands it runs, or a built-in template (launchway templates list) |
| filters with regular expressions or ignore lists | The filter … uses a regular expression or an ignore list, which Launchway patterns cannot express | List the branch or tag names (a * pattern is allowed), then add a when: filter |
| context (shared variables and secrets) | The job uses the context …, which holds variables and secrets in the old system | Create the variables and secrets it holds in Launchway and name the secrets under the step's secrets |
| A workflow job with matrix | The matrix of job … fills job parameters, which are not carried | Write for_each on the step with the values the commands read |
| Pipeline parameters and setup workflows | The file uses …, which makes the pipeline from other input at run time | Write the pipeline out in full, or use run inputs |

## Stage-based pipeline file

Where it lives: a file named .*-ci.yml at the top of the repository. Import it with `launchway import pipeline --from stages-yaml`.

44 constructs are known: 16 convert exactly, 12 convert with a difference, 16 need a person.

### Stage-based pipeline file: Converts exactly

| In the old file | What the importer does | What you do |
|---|---|---|
| stages (the order of the stages) | Each job waits for the jobs of the stage before its own; jobs of one stage run together | Nothing. |
| A job with script, before_script and after_script | The job became a step; before_script, script and after_script run in that order in one script | Nothing. |
| default (image, services, cache, retry, timeout, before_script) | Defaults were written into each job that does not set its own | Nothing. |
| image | The image became the step's image | Nothing. |
| variables (global and per job) | Variables written in the file carry over as environment variables | Nothing. |
| rules with if on branch, tag or pipeline source, and changes | The rules became a when: filter | Nothing. |
| when: always, on_failure, on_success | The job runs after the jobs before it, as the old value said (upstream: always or failed) | Nothing. |
| needs (jobs out of stage order) | The job needs exactly the jobs named, whatever their stage | Nothing. |
| allow_failure | The job may fail without failing the run (on_failure: continue) | Nothing. |
| retry | Failed attempts are tried again as often as written (at most 10) | Nothing. |
| timeout | The time limit carries over | Nothing. |
| parallel: matrix | The matrix became for_each; each key is also an environment variable | Nothing. |
| extends, YAML anchors, merge keys and !reference | Templates, anchors and references were resolved and written out in full | Nothing. |
| include: local | The included file … was read and merged | Nothing. |
| artifacts (paths, name, expire_in, when) | The files became cargo of the step, and jobs that need this one fetch them | Nothing. |
| artifacts:reports junit and coverage_report | Test and coverage reports became reports of the step | Nothing. |

### Stage-based pipeline file: Converts with a difference

| In the old file | What the importer does | What you do |
|---|---|---|
| after_script | after_script runs as the last lines of the script, so it does not run if an earlier line fails, and it shares the script's shell | Put the clean-up in a trap, or in a step that runs when the job before it failed |
| services (name, alias, command) | The service … runs beside the step, but it is reached at localhost, not by its alias or host name | Change the host name your tests use to localhost |
| A variable set in the old system's settings | The script reads …, which is not set in the file; it was probably set in the old system's project settings, so it was read by name from a secret store | Create the secret or variable in Launchway (names only), or remove it if it is not a secret |
| Predefined variables (CI_COMMIT_SHA, CI_COMMIT_BRANCH, CI_COMMIT_TAG, ...) | The variable … was replaced by … | Check the value reads the same where you use it |
| when: manual | A manual job became a step that waits behind an approval, but the old job did not block the pipeline | Check the approvers, and whether later steps should wait for it |
| retry with when or exit codes | The job retries after any failure, not only the listed reasons | Check that retrying every failure is acceptable |
| parallel: N | The job became a loop of … members; the script gets CI_NODE_INDEX and CI_NODE_TOTAL as variables, as before | Check the numbering (it starts at 1 in both) |
| cache (paths, key, key:files) | The cache for … is kept between runs by Launchway under its own rules for branches and pull requests | Check the key files and paths |
| cache with untracked, policy or fallback keys | The cache setting … was not carried; Launchway restores before and saves after every successful step | Nothing, unless a pull-only cache mattered |
| environment (name, url, action, on_stop) | The job deploys to the environment …, so it is marked as delivering; protection and review rules of the old environment were not read | Add an approval, or protect the Airfield |
| workflow:rules | Pipeline-level rules decide when a pipeline starts; the triggers below were written from the jobs' own rules, or as every branch and tag push | Check the on: section |
| tags (runner tags) | Runner tags … are not carried; Launchway chooses the Station by its own configuration | Use the runner key of the step to pick a Station |

### Stage-based pipeline file: Needs a person

| In the old file | What the importer does | What you do |
|---|---|---|
| image with entrypoint | The image entrypoint … is not carried: Launchway runs the script with sh in the image | Use an image whose own command does not need the entrypoint, or call it from the script |
| Other predefined variables (CI_REGISTRY, CI_PROJECT_*, CI_JOB_TOKEN, ...) | The script uses …, which only the old system sets | Set it yourself as an environment variable of the step, or use the Launchway variable shown in the environment variables reference |
| rules with other expressions, regular expressions, never rules, only/except | The condition `…` cannot be written as a when: filter, so the job now always runs | Write the test as a when: filter (branch, tag, event, changed, input), or in the script |
| when: delayed, start_in | A delayed start is not carried | Put a sleep in the script, or start the pipeline from a schedule |
| needs from another project or pipeline, optional needs | The dependency … is not in these files | Remove it or make the other pipeline start this one with a webhook |
| allow_failure with exit_codes | Allowing only certain exit codes is not carried; the job is not allowed to fail | Handle the exit code in the script and exit 0 |
| A timeout that is not a plain number of minutes or hours | The timeout `…` could not be read as a duration | Write it as a duration such as 90m on the step |
| parallel: matrix with several entries | Several matrix entries each make their own combinations, which one loop cannot hold | Write one step for each entry |
| include: remote, project, template, component | The include … is not in these files and is never fetched here | Copy its content into the repository and add it to the files you import, or write a template |
| artifacts with exclude, expose_as, untracked, other reports | The artifact setting … is not carried | Convert scanner reports to SARIF and list them under reports: findings |
| environment action: stop | A job that stops an environment has no equivalent step kind | Use a Return to base step, or run the clean-up by hand |
| trigger (child and downstream pipelines) | The job starts another pipeline, which has no equivalent in a pipeline file | Import the other pipeline, or start it with a webhook |
| coverage (a regular expression on the log) | Reading a coverage number from the log is not carried | Write a coverage report file and list it under reports: coverage |
| secrets (from a vault) | The job reads secrets … from a vault by a provider block, which Launchway does not run | Create the secrets in the Station's store and name them under the step's secrets |
| resource_group | Jobs that must not run at the same time (…) are not carried | Add a lane to the pipeline if whole runs must take turns |
| Any other job keyword | The keyword … is not carried | Check what it did in the old file and write it by hand |

## Tasks-and-stages pipeline file

Where it lives: a file named *-pipelines.yml at the top of the repository. Import it with `launchway import pipeline --from tasks-pipelines`.

33 constructs are known: 13 convert exactly, 10 convert with a difference, 10 need a person.

### Tasks-and-stages pipeline file: Converts exactly

| In the old file | What the importer does | What you do |
|---|---|---|
| trigger (branches, tags, paths) and trigger: none | The push trigger carries over with its branch, tag and path filters | Nothing. |
| schedules (cron) | The cron times became schedule triggers; the branch list was not carried, so they run on the default branch | Nothing. |
| stages with dependsOn | Each stage waits for the stage before it, or the stages named in dependsOn | Nothing. |
| job with steps | The job became a step; its scripts run in order in one script | Nothing. |
| dependsOn on a job | Dependencies between jobs carry over | Nothing. |
| condition: always(), failed(), succeeded() | The condition became the upstream setting of when: | Nothing. |
| variables (name and value) and parameters | Variables carry over as environment variables; parameters became run inputs | Nothing. |
| script, bash | Commands carry over; workingDirectory and env wrap that step's commands | Nothing. |
| checkout | Launchway checks the repository out before the first step | Nothing. |
| NodeTool, UsePythonVersion, GoTool and similar version tasks | The version became the step's image (…) | Nothing. |
| PublishBuildArtifacts, PublishPipelineArtifact, publish, download | The files became cargo of the step, and a download became a fetch | Nothing. |
| PublishTestResults, PublishCodeCoverageResults | Test and coverage results became reports of the step | Nothing. |
| continueOnError and timeoutInMinutes | On a job, they carried over as on_failure: continue and a time limit; on a step they are not carried | Nothing. |

### Tasks-and-stages pipeline file: Converts with a difference

| In the old file | What the importer does | What you do |
|---|---|---|
| trigger exclude lists and batch | Launchway cannot exclude branches or tags by pattern, so … was not carried; path exclusions use paths_ignore | List the branches or tags that should run instead of the ones that should not |
| No trigger section | The old system runs on every branch when a file has no trigger section, so every branch push was written as a trigger | Narrow the branches if you do not want that |
| pr | Pull requests run at their head commit, filtered by the branch they target; pr: none turns them off, and autoCancel and drafts are not carried | Check the branch list |
| pool (vmImage, name) | The pool … is not carried: Launchway chooses the Station by its own configuration and runs steps in a container image | Name the image the job needs |
| container and services (container resources) | The container … became the step's image or a service; services are reached at localhost, not by their resource name | Change the host name your tests use to localhost |
| $(Build.SourceVersion), $(Build.BuildId) and other predefined variables | The variable … was replaced by … | Check the value reads the same where you use it |
| $(name) of a variable that is not in the file | The script reads …, which is not set in the file; it was probably a secret variable set in the old system, so it was read by name from a secret store | Create the secret in Launchway (names only), or set the variable if it is not a secret |
| Cache task | The cache for … is kept by Launchway between runs; the key's file names became a file list, and other parts of the key were dropped | Check the key files and paths |
| Docker task (build, push, buildAndPush) | The image build became an image step; it pushes to Launchway's built-in registry, not the service connection the old file named | Check the tags, and name another registry in the step if you push elsewhere |
| deployment jobs with an environment and runOnce | The deployment to environment … became an ordinary step marked as delivering; approvals and checks set on the environment in the old system were not read | Add an approval step or protect the Airfield |

### Tasks-and-stages pipeline file: Needs a person

| In the old file | What the importer does | What you do |
|---|---|---|
| condition on a job or step | The condition `…` is an expression, which Launchway does not evaluate, so it always runs | Express it as a when: filter (branch, tag, event), or in the script |
| variable groups and templates | The variable group or template … lives outside this file | Create its variables in Launchway (names only; secrets as secrets) or copy the template's content here |
| ${{ parameters.name }} and other template expressions | The expression `…` is evaluated when the old file is read, and is left as written | Write the value in place, or use a run input |
| pwsh, powershell | The step runs PowerShell, which is not in a plain Linux image | Use an image that has pwsh and run the script with it, or rewrite it for sh |
| A cache of a directory outside the workspace | The cache path … is outside the workspace, and Launchway keeps only paths below it | Make the tool use a directory below the workspace and cache that |
| template (steps, jobs or stages from another file) | The template … is another file, which is not read here | Import it as well and copy its steps here, or write a Launchway template |
| Any other task | The task … is a packaged tool that Launchway does not know | Replace it with the commands it runs, or ask a person to decide |
| deployment hooks (preDeploy, routeTraffic, canary, rolling) | The deployment strategy … is not carried; only the deploy steps are | Use a rollout step for canary and rolling releases, or add the steps by hand |
| strategy matrix, parallel | The matrix is a set of named variable sets, which a Launchway loop of lists cannot hold as it is written | Write for_each with a list for each variable, or one step for each named set |
| resources (repositories, pipelines, containers) | The resources … are other repositories or pipelines, which are not read | Fetch other repositories in the script, or start the other pipeline with a webhook |

## What every import adds

These notes come from the writer that makes the Launchway file, whatever the source format.

| Situation | What the importer does | What you do |
|---|---|---|
| A job that is the standard job of a language | Step … uses the built-in template launchway/… instead of your own commands: it runs the standard commands and also records test and coverage reports | Compare its log with the old job's. To keep your own commands, write the step by hand (the wizard has a switch) or use the template's test_command setting |
| A job with no image | The steps … name no container image, so they run in … instead of a hosted runner's machine image, which has many tools installed | Name an image that has the tools the commands need (set image: on the step), or install them in the script |
| A secret used by name | Secret … is read from the Station's secret store %q by name, instead of from the old system's own storage; no value was copied | Create the secret in the store under this name (names only here; the value is never in these files) |
| A step that delivers, in a dry import | Step … delivers (…) and is switched off by the dry import: it is skipped until the input enable_delivery is true | Compare results with the old system first, then set the default of the input enable_delivery to true |
| Pushing an image | Step … pushes the image to repository … of Launchway's built-in registry instead of the registry the old file named | To push elsewhere, name a registry your Stations list in the step's tags (registry:) |
| A script that uses bash features | Step … runs with sh -e in Launchway but the old file used bash features, so a line may fail instead of running as before | Use an image that has bash and write bash -c '...' around the script, or rewrite the lines for sh |
| A matrix value Launchway cannot loop over | The matrix key … has a name or a value that Launchway's loops do not allow (letters, digits and . _ + - only), so the job is written without it | Rename the value, or write one step for each combination |
| A dependency on a job that was not found | The job needs …, which is not in the files that were read (needed by …) | Add the file that defines it, or remove the dependency |
| Downloading an artifact nobody keeps | The job downloads the artifact …, but no job it depends on keeps one by that name | Make the job that produces it a dependency, or add the files to the job that uses them |
| Approvers named by user | Step … asks the users named in the old file, as written there; Launchway matches them against its own user names | Check the names are Launchway users, or use groups |

<!-- importer-tables:end -->

## What next

- [Your first pipeline](../tutorials/first-pipeline.md) and the [pipeline reference](../reference/pipeline.md) for the key each finding points to.
- [Starters](starters.md) for the built-in templates the importer uses.
- [Secrets](secrets.md) and [Variables](variables.md) for the names a proposal asks you to create.
