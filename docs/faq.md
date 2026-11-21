# Frequently asked questions

Short answers, with links to the details.

## Is there a hosted version or a public demo

No. You run Launchway yourself. The [quick start](start/quick-start.md) runs a demo on your
machine in a few minutes, and the [tour](demo.md) shows screenshots of a real instance.

## What does it need to run

One binary. With no database setting it keeps everything in an embedded SQLite file, which suits
one server. Several servers need PostgreSQL. Steps need Podman, Docker or Kubernetes. See
[How-to guides](howto/index.md).

## Is it production ready

It is early software. What is described as available works and is tested, including with several
servers and real Kubernetes, PostgreSQL, OpenBao, BuildKit and Argo CD. Read the
[current limits](security/trust-model.md#current-limits-and-what-is-changing) before relying on it.

## Is Launchway PCI DSS or HIPAA compliant

Software is not compliant on its own and Launchway is not certified. It provides controls and
evidence that can support an organization's own compliance. See
[Controls and compliance](security/controls-and-compliance.md).

## Does the server see my secrets

No. Pipelines hold references; runners look values up in your secret stores and mask them in
logs. See [Secrets](guide/secrets.md).

## Do runners need inbound network access

No. Runners only connect out to the server, over HTTP or HTTPS. See
[Connect a runner](howto/connect-runner.md).

## Which Git hosts work

Any Git server Launchway can fetch from. Pull requests work with hosts that publish
`refs/pull/<number>/head`, such as GitHub, Gitea and Forgejo. Webhooks are understood from GitHub,
Gitea, Forgejo and GitLab. See [Triggers](guide/triggers.md).

## Can it deploy without Argo CD

Not yet. Deploy steps work through Argo CD and, for canaries, Argo Rollouts. Other deploy targets
are on the [roadmap](roadmap-list.md). A plain step can still run any deployment script you write.

## Is there an expression language in pipelines

No, on purpose. Values come from a few typed forms: outputs, run values, inputs, loop values and
template parameters. See the [pipeline reference](reference/pipeline.md).

## Can I run a pipeline without a server

Yes: `launchway run` in a repository runs its pipeline on your machine with the same engine. See
[First pipeline](tutorials/first-pipeline.md).

## How do I report a security problem

Privately, as described in [Reporting a vulnerability](security/reporting.md).
