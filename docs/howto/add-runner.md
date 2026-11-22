# Add a Station (runner) {#add-a-runner}

A Station is a machine process that runs the steps of your pipelines. The wizard prepares a pool for
it, makes a one-time join token and shows the exact command to run.

## What it asks

1. **Which pool?** A pool is a group of Stations. A *build* pool runs the steps of Flights (codebase), optionally
   only for some events; a *deploy* pool serves deploy targets (a cluster) and may deploy only to
   them. Pick an existing pool or create one. For a new pool the wizard asks which Flights (or
   targets) it serves, and whether Stations must enrol with a join token of their own (recommended: a
   shared token cannot be told apart from another machine using it).
2. **Where will it run?** On Kubernetes, on a virtual machine or server, or in a container. The
   wizard shows the command that fits.
3. **The join token.** The wizard makes a token for the pool, valid for one Station for one hour, and
   shows it **once**. Copy it into the install command. Tokens are never shown again; make another if
   it is lost.

## Then

The wizard shows the command, and waits: when the Station connects, the page says so, with its name,
pool and version. If nothing happens, it says what to check (the server address must be reachable from
the Station, and the clock must be right).

## What it creates

The pool (if new) and the token. Cancelling before the last step creates nothing. Stations are listed in
**Stations**, where an enrolled Station can be revoked at once.

From the command line: `launchway runner-pools set NAME -f pool.yaml`, `launchway Stations join-token
--pool NAME`.
