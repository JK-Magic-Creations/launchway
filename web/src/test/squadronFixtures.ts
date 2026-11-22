import type { MovePlan, MovePreview, OrganizeProposal, SquadronList } from "../squadronsApi";

/** Acme-style Squadrons and Flights as the server answers them, for the move and organize tests. */
export const flightRow = (name: string) => ({ name, favorite: false, running: 0, needs: 0, history: [] as string[], pipelines: 1 });

export function squadronList(over: Partial<SquadronList> = {}): SquadronList {
  return {
    squadrons: [
      { name: "payments", display_name: "Payments", flights: 2, needs: 0, failing: 0, running: 0 },
      { name: "storefront", display_name: "Storefront", flights: 1, needs: 0, failing: 0, running: 0 },
    ],
    unfiled: [flightRow("shop-api"), flightRow("shop-web"), flightRow("docs")], may_manage: true, ...over,
  };
}

export const plan = (flight: string, over: Partial<MovePlan> = {}): MovePlan => ({
  flight, from: "", to: "payments", stops: [], starts: [], variables: [], secrets: [], access: [], references_from: "the pipeline file", blocked: false, reasons: [], ...over,
});

/** shop-api moves with changes, shop-web is blocked by a secret that would resolve to a different store. */
export function movePreview(): MovePreview {
  return {
    to: "payments", blocked: 1, plans: [
      plan("shop-api", {
        starts: [{ kind: "policy", name: "require-review", from: "payments" }], stops: [{ kind: "variable", name: "registry", from: "Unfiled" }],
        variables: [{ name: "region", before: "us-east", after: "eu-west" }],
        access: [{ who: "ana", before: "viewer", after: "operator", effect: "gains" }, { who: "bo", before: "operator", after: "viewer", effect: "loses" }],
        secrets: [{ store: "vault:apps/registry", before: "vault", after: "vault", result: "same" }],
      }),
      plan("shop-web", { blocked: true, reasons: ["The secret vault:apps/web would resolve to a different store in payments."], secrets: [{ store: "vault:apps/web", before: "vault", after: "kms", result: "different" }] }),
    ],
  };
}

export const proposal = (): OrganizeProposal => ({
  unfiled: ["shop-api", "shop-web", "payments-api", "payments-ledger", "docs"],
  groups: [{ name: "shop", flights: ["shop-api", "shop-web"], source: "prefix" }, { name: "payments", flights: ["payments-api", "payments-ledger"], source: "folder" }], leftover: ["docs"],
});

/** The GET answer of the Organize proposal, for installServer (the Squadron lists are in mockApi's own squadronFixtures). */
export function moveFixtures(): Record<string, unknown> {
  return { "/api/v1/unit-organize": proposal() };
}

/** A fetch stub that tells methods apart: routes are "METHOD /path". It records every call. */
export function routeFetch(routes: Record<string, unknown | ((body: unknown) => unknown)>) {
  const calls: { key: string; body: unknown }[] = [];
  const f = async (input: RequestInfo | URL, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${String(input).split("?")[0]}`;
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ key, body });
    const hit = routes[key];
    if (hit === undefined) return new Response(JSON.stringify({ error: `no route ${key}` }), { status: 404, headers: { "Content-Type": "application/json" } });
    const out = typeof hit === "function" ? (hit as (b: unknown) => unknown)(body) : hit;
    return new Response(JSON.stringify(out), { status: 200, headers: { "Content-Type": "application/json" } });
  };
  return { f, calls };
}
