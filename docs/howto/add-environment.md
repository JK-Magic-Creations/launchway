# Add an Airfield (environment) {#add-an-environment}

An Airfield is where deploy steps may deploy: staging, production, a cluster. Deploys to it are
refused unless it exists, which is how Launchway keeps a pipeline from deploying somewhere nobody
agreed to.

## What it asks

1. **What is it called, and where is it?** A name for the Airfield and its *target*, the name
   deploy Stations (runners) use for the place they serve (usually a cluster). A deploy Station serves one or more
   targets.
2. **Which projects may deploy here?** Names, or patterns such as `shop-*`.
3. **Protect it?** On a *protected* Airfield, the person who started a Sortie (run) may not promote its
   deployment, and anyone with the operator role there may abort it. Use it for production.
4. **What if several Sorties want to deploy?** Only one deployment runs at a time. *Newest* skips a
   waiting run when a newer one of the same service arrives; *all* keeps every one in order.
5. **Approvals.** A note on where approvals come from: an approval step in the pipeline, and the roles
   that may decide it (see [Roles](../reference/roles.md)). Airfields can also be restricted per
   role binding.

## What it creates

The Airfield; and, if you ask, a deploy pool for its target. A summary shows both before anything
is created.

From the command line: `launchway environments set prod -f prod.yaml`.
