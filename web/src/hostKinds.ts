import { useEffect, useState } from "react";
import { outsideApi, type HostMatrix } from "./outsideApi";

// What a person must be told about each kind of outside Git host (its name, what it calls a change request, whether
// its address must be given, the steps of its webhook form) comes from the server, in the capability matrix, which
// is made from the kinds the server has. There is no table of kinds here: a kind the server adds appears with its
// own words, and one it lacks does not.

export type HostKind = HostMatrix["kinds"][number];

let cached: Promise<HostKind[]> | undefined;

/** Forget what was read (for tests, which serve different kinds). */
export function resetHostKinds() { cached = undefined; }

function loadKinds(): Promise<HostKind[]> {
  cached ??= outsideApi.matrix().then((m) => m.kinds ?? []).catch((e) => { cached = undefined; throw e; });
  return cached;
}

/** The kinds the server offers (empty until they are read; a failure leaves it empty, and the pages say "this host"). */
export function useHostKinds(): HostKind[] {
  const [kinds, setKinds] = useState<HostKind[]>([]);
  useEffect(() => {
    let live = true;
    loadKinds().then((k) => { if (live) setKinds(k); }).catch(() => {});
    return () => { live = false; };
  }, []);
  return kinds;
}

/** The steps of the host's webhook form. */
export const webhookSteps = (k?: HostKind) => [
  ...(k?.no_content_type ? [] : ["Content type application/json."]),
  `The secret you stored for this connection goes in ${k?.secret_field ?? "Secret"}.`,
  `Events: pushes, tag pushes and ${k?.change ?? "pull request"}s.`,
];

export function hostKind(kinds: HostKind[], kind: string | undefined): HostKind | undefined {
  return kinds.find((k) => k.kind === kind);
}
