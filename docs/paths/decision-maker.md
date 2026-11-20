# Path for business readers and decision makers

What you need to know to decide whether Launchway fits, in about fifteen minutes, without
technical detail.

## What it is

Launchway automates the work between a developer's change and that change running in production:
building, testing, security checks, approvals and deployment. It runs on your own infrastructure.
See [What Launchway is](../start/overview.md).

## Why it matters

- **Fewer failed releases.** Every change goes through the same tests and checks; a failed
  deployment is rolled back automatically.
- **Evidence.** Every run records what was built, how it was checked and who approved it.
  Policies make the important checks mandatory, with recorded, expiring exceptions.
- **Control.** Code, secrets and records stay on infrastructure you choose. Secret values never
  reach the Launchway server.
- **Resilience.** Several servers can share the work; losing one loses nothing.

## How mature it is

Launchway is early, open-source software (Apache 2.0). The features these documents describe as
available work and are tested. Others are in progress or planned; the
[roadmap](../roadmap-list.md) shows each one's status. There is no hosted service and no paid
support offering today.

## Questions to ask your team

- Who will run it, and on what: [one server or Kubernetes](../howto/index.md)?
- Which identity provider will people sign in with?
- Which controls do we need for audits? See [Controls and compliance](../security/controls-and-compliance.md).
- Are the [current limits](../security/trust-model.md#current-limits-and-what-is-changing) acceptable for us until they are fixed?

## See it

- [Screenshot tour](../demo.md).
- [Quick start](../start/quick-start.md): a developer can show it running in a few minutes.
