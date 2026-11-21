// Package storagespike is the executable spike of story S34-08: it builds,
// in their smallest honest form, the two candidate designs for hosting Git
// repositories on several servers, and attacks both with the same failure
// injection and the same properties. It is not wired into the server and
// holds no production code; its results are written up in
// docs/development/forge-storage-spike.md and decide what S34-09 builds.
//
// The candidates:
//
//	A  "volume": plain bare repositories on a persistent volume, written
//	   only by the real git binary (receive-pack, with git's own atomic ref
//	   transactions), plus a ledger of ref updates in the database for events
//	   and audit, and off-node copies made by mirroring.
//	B  "ledger": the design of doc 26 section 5.4: refs and a pack manifest in
//	   the database, immutable packs in a blob store, a disposable per-replica
//	   cache of bare repositories checked against a generation number. The
//	   push commit point is a database compare-and-swap inside git's
//	   reference-transaction hook.
//
// Both run real git (receive-pack, upload-pack) through internal/gitexec,
// with the hooks installed as symlinks to the test binary (the production
// design: no shell, no scripts in the repository), and every failure is
// injected inside those real processes, by name, through the environment.
//
// Run length: the default test run is a smoke run (the injection campaign does 24 rounds, 4 under the
// race detector, where every hook start costs seconds); the whole package finishes in about a minute
// on two cores. The full campaign is opt-in: FORGE_SPIKE_ROUNDS=100 (the report ran 1,150), with
// FORGE_SPIKE_SEED to vary it and FORGE_SPIKE_MEASURE=1 for the measurements. See
// docs/development/forge-storage-spike.md.
package storagespike
