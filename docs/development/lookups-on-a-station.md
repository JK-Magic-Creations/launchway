# Moving the lookups to a Station

Status: design note; not built. `maintenance.lookups` is `server` or `off` today.

## What is looked up, and from where

The server looks up newer versions at package registries for Maintenance (`internal/depupdate/source`) and advisories at the advisory service for the software graph (`internal/depupdate/osv`, wired in `cmd/launchway/graph_wire.go`). Both go through the egress guard's client. A server whose network is closed for outbound traffic cannot make them, which is why the server now leaves a host alone for an hour after a refused or timed-out connection, and why `maintenance.lookups: off` exists.

## Why a Station

A Station is the part of an installation that is allowed to reach the outside. Where the server is closed in and the Stations are not, the lookups belong there: the server asks, a Station answers, and the answers are cached by the server as they are today.

## The seam that is missing

There is no kind of job for "answer these lookups". A Station leases steps of a Sortie; a lookup is not a step, has no Sortie, and its answer is data for the server rather than files or a result. Adding one needs:

1. A job kind (`lookup`) with its own lease, so that a Station that does not know it never takes it (the rolling-update rule: an older Station ignores kinds it does not know).
2. A small request and answer shape: the source name, the package, the answer bytes, limited in size like any answer from a package host.
3. The server's `Fetcher` taking an interface instead of an HTTP client, with the present client as one implementation and the Station as another.
4. Egress rules: the Station's own guard decides what it may reach; the server records which Station answered.
5. Rights: only a Station of the installation may answer, with its own credential; the answer is data, never run.

Until then `maintenance.lookups: station` is not offered: a setting that no code reads would be a switch that does nothing.
