# Switches (feature toggles) {#feature-toggles}

A **toggle** is a value your application reads while it runs, so that a feature can be turned on, off or
rolled out to some of your users without a new deployment. Shipping the code and turning it on become two
separate steps, and turning it off again takes seconds.

| Without a toggle | With a toggle |
|---|---|
| The feature goes live when the code does. | The code goes out with the feature off. You turn it on when you are ready. |
| Something is wrong: you roll back the whole release. | You turn the toggle off. Nothing else moves. |
| Everyone gets it at once. | 5% of users get it, then 25%, then everyone. |

## What a toggle does in an Airfield (environment) {#what-a-toggle-does-in-an-environment}

Each Airfield (dev, staging, production) has its own state for a toggle: **off**, **on for everyone**,
**on for people who match rules** (the paying plan, a version of the app, a list of beta testers), or **on for a
percentage**. A percentage is **sticky**: a person lands in the same share every time, so going from 5% to 25%
keeps the first 5% and adds more.

## Your team in charge, with safeguards on

Changing a toggle in production is a production change. So by default every change needs a **reason**, and in a
protected Airfield it waits for **someone else to approve**. Your team can turn these down, with a reason that is
recorded; a level above can require them. Going back is never held up: **revert** on any change in the history puts
things as they were.

## What changed before it broke

Toggle changes sit on the Airfield's page beside its deployments, and the Return to base (rollback) page lists the toggle changes
made since the version you are going back to, with a choice to revert them together with the Return to base. A Return to base
restores a version, not a toggle, so Launchway tells you rather than guessing.

## Reading a toggle in your code

Small libraries for Go and TypeScript keep the rules in memory and evaluate in your application, so a toggle costs
no network call and nothing about your user leaves it. Read the [guide](../../guide/feature-toggles.md), then the
[SDK quick starts](../../guide/toggles-sdks.md).

## What to remember

- A toggle is not a security boundary: what a browser can read, anyone can read.
- Toggles are meant to be temporary. Launchway notes the ones that look finished (on for everyone for a month, or
  unused), and never removes one itself.
