import { useState } from "react";
import { configApi, type Spec } from "../configApi";
import { SpanMono } from "../kit/bits";

/**
 * AllowHost explains why the server refused an address (it is inside the
 * server's own network) and lets an administrator add the host to the
 * allowed hosts, then run the check again. It is shown only where a check
 * answered "blocked".
 */
export function AllowHost({ host, onAllowed }: { host: string; onAllowed?: () => void }) {
  const [state, setState] = useState<"idle" | "busy" | "done" | "error">("idle");
  const [error, setError] = useState("");
  const add = async () => {
    setState("busy"); setError("");
    try {
      let version = 0;
      let spec: Spec = {};
      try { const o = await configApi.get("installation", "installation"); version = o.version; spec = o.spec; } catch { /* never written: created below */ }
      const egress = (spec.egress as { allow?: string[] } | undefined) ?? {};
      const allow = Array.from(new Set([...(egress.allow ?? []), host]));
      await configApi.put("installation", "installation", { spec: { ...spec, egress: { ...egress, allow } }, version, comment: `allow ${host}: the server may contact it` });
      setState("done");
      onAllowed?.();
    } catch (e) {
      setState("error"); setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <div className="callout callout-info" role="note">
      <p><strong>The server does not contact addresses inside its own network</strong> (this stops anyone who can reach it from using it to probe that network). <SpanMono>{host}</SpanMono> is one.
        If it is your own server, you can allow it: the server may then contact exactly that host.</p>
      {state === "done"
        ? <p className="cf-ok">Added <SpanMono>{host}</SpanMono> to the allowed hosts. Check again.</p>
        : <button type="button" className="btn" disabled={state === "busy"} onClick={add}>{state === "busy" ? "Adding…" : `Add ${host} to the allowed hosts`}</button>}
      {state === "error" && <p className="cf-problem">That did not work: {error}</p>}
    </div>
  );
}
