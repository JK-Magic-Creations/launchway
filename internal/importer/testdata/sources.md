# Where each construct's syntax comes from

The readers take the syntax of each format only from that format's official public
reference, read on 2026-11-22 (read-only). This file is in testdata so that the
reference sites can be named; product files describe the formats without naming them.
No documentation text or example file was copied: the fixtures next to this file are
written for Launchway.

## hosted-workflows (hosted-workflow files)
- Reference: https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
  - on, push/pull_request filters (branches, branches-ignore, tags, paths, paths-ignore), schedule, workflow_dispatch inputs,
    workflow_call, workflow_run: sections "on", "on.push.<branches|tags|branches-ignore|tags-ignore>.", "on.schedule", "on.workflow_dispatch.inputs"
  - concurrency, permissions, env, defaults.run: top-level sections of the same page
  - jobs.<job_id>: needs, if, runs-on, environment, outputs, env, timeout-minutes (default 360), strategy (matrix, include, exclude, fail-fast default true, max-parallel), continue-on-error, container, services, uses, with, secrets
  - steps: id, if, name, uses, run, shell, with, env, continue-on-error, timeout-minutes, working-directory
  - expressions: `${{ }}`, contexts matrix, inputs, secrets, vars, env, github
- Marketplace steps are mapped by name only (checkout, setup-go/node/python/java/dotnet, ruby setup, cache, upload-artifact,
  download-artifact, docker build-push, docker login, docker metadata, docker buildx/qemu setup); each action's own README states its inputs.

## stages-yaml (stage-based pipeline file)
- Reference: https://docs.gitlab.com/ci/yaml/ (anchors: #stages, #default, #include, #workflow, #script, #rules, #needs, #extends,
  #parallel, #parallelmatrix, #cache, #artifacts, #environment, #retry, #timeout, #allow_failure, #image, #services, #trigger, #variables, #secrets, #resource_group, #coverage, #tags)
- only/except: https://docs.gitlab.com/ci/yaml/deprecated_keywords/
- Predefined variables: https://docs.gitlab.com/ci/variables/predefined_variables/

## groovy-declarative (declarative Groovy pipeline file)
- Reference: https://www.jenkins.io/doc/book/pipeline/syntax/ (anchors: #declarative-pipeline, #agent, #stages, #steps, #environment, #options,
  #parameters, #triggers, #tools, #input, #when, #post, #parallel, #declarative-matrix, #sequential-stages)
- Steps (sh, bat, dir, withEnv, timeout, retry, archiveArtifacts, junit, stash, input, withCredentials): https://www.jenkins.io/doc/pipeline/steps/
  and https://www.jenkins.io/doc/pipeline/steps/workflow-basic-steps/

## jobs-config (jobs-and-workflows config file)
- Reference: https://circleci.com/docs/reference/configuration-reference/ (anchors: #executors, #commands, #jobs, #docker, #machine, #resourceclass,
  #steps, #workflows, #filters, #branches, #tags, #matrix, #requires, #type, #context)

## tasks-pipelines (tasks-and-stages pipeline file)
- Reference: https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/ (pages: trigger, pr, schedules, stages-stage, jobs-job,
  jobs-deployment, jobs-deployment-strategy-run-once, steps-script, steps-task, steps-checkout, steps-publish, steps-download, variables, parameters, pool, resources)
