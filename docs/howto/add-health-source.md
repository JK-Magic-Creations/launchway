# Add a health source

A health source is a metrics or logs system that verify steps may read to decide whether a deployment
is healthy: Prometheus, Loki, ClickHouse, Elasticsearch or OpenSearch, PostgreSQL, or an HTTP
endpoint. Pipelines refer to it by name.

## What it asks

1. **Which kind of system, and what do you call it?** The name pipelines will use.
2. **Which projects may use it?** Names or patterns. A project not listed cannot read the source.
3. **Where is it, and how does Launchway sign in?** This is the part that is *not* stored here: the
   address and credentials stay with the deploy runners, which are the ones that query the system
   (see [Verifying a deployment](../guide/verification.md)). The wizard tells you which runner setting
   to make so that the name resolves, and does not ask for a password.

## What it creates

The definition (name, type, projects). Until a deploy runner has the matching connection, a verify step
that names the source fails with a message saying so.

From the command line: `launchway health-sources set prometheus -f source.yaml`.
