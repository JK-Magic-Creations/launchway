import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api";
import { v2bApi, type ApprovalSettings } from "../api/v2b";
import { useMe } from "../auth";
import { configApi, loadOverview, type ConfigObject } from "../configApi";
import { ErrorBox } from "../components/ui";
import { useLoad } from "../guided/useLoad";
import { Button, PageHead, Sheet } from "../kit/layout";
import { Icon } from "../kit/Icon";
import { terms } from "../terms";

type Choice = "unset" | "yes" | "no";

/** Settings → Approvals: whether the person who starts a run may approve it. The page says in plain words what
 * each choice means and which one is in force; an administrator can change it here, and the change is versioned
 * with the rest of the installation settings. */
export function SettingsApprovalsPage() {
  const me = useMe();
  const readFn = useCallback(() => v2bApi.approvalSettings(), []);
  const now = useLoad<ApprovalSettings>(readFn);
  const [obj, setObj] = useState<{ kind: string; name: string; o: ConfigObject | null } | null>(null);
  const [choice, setChoice] = useState<Choice>("unset");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const admin = !!me?.admin;

  useEffect(() => {
    if (!admin) return;
    let alive = true;
    (async () => {
      try {
        const ov = await loadOverview();
        const k = ov.kinds.find((x) => x.name === "installation");
        if (!k || !k.singleton) return;
        let o: ConfigObject | null = null;
        try { o = await configApi.get(k.path, k.singleton); } catch (e) { if (!(e instanceof ApiError && e.status === 404)) throw e; }
        if (!alive) return;
        setObj({ kind: k.path, name: k.singleton, o });
        const v = (o?.spec?.approvals as { allow_self?: boolean } | undefined)?.allow_self;
        setChoice(v === true ? "yes" : v === false ? "no" : "unset");
      } catch (e) { if (alive) setError(e); }
    })();
    return () => { alive = false; };
  }, [admin]);

  const save = async () => {
    if (!obj) return;
    setBusy(true); setError(null); setSaved(false);
    try {
      const spec = { ...(obj.o?.spec ?? {}) } as Record<string, unknown>;
      const approvals = { ...((spec.approvals as Record<string, unknown> | undefined) ?? {}) };
      if (choice === "unset") delete approvals.allow_self; else approvals.allow_self = choice === "yes";
      if (Object.keys(approvals).length) spec.approvals = approvals; else delete spec.approvals;
      const r = await configApi.put(obj.kind, obj.name, { spec, version: obj.o?.version ?? 0, unit: obj.o?.unit || undefined, comment: "Approvals: who may approve their own runs" });
      setObj({ ...obj, o: r.object }); setSaved(true); now.reload();
    } catch (e) { setError(e); } finally { setBusy(false); }
  };

  const s = now.data;
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title={terms().plural("clearance")}
        sentence={s === null ? "Loading." : s.allow_self
          ? `The person who starts a run may give ${terms().lower("clearance")} for it themselves.`
            : terms().say(`The person who starts a {run} cannot give ${terms().lower("clearance")} for it: someone else must.`)} />
      <ErrorBox error={now.error} />
      <Sheet title={terms().say(`Who may give ${terms().lower("clearance")} for a {run}`)} pad>
        <div className="stack">
          <p>
            A step that needs {terms().lower("clearance")} stops a run until a person says {terms().label("go")}. By default, <b>the person who started the run may not give it</b> once people sign in,
            so a second pair of eyes always looks at a change before it goes further. They may still say {terms().label("no_go")}.
            Without sign-in everyone is the same person, so giving {terms().lower("clearance")} for your own run is allowed.
          </p>
          <p className="faint">
            {terms().say("This also decides who may promote or retry a deployment on a protected {environment}. In force now:")}{" "}
            <b>{s === null ? "…" : s.allow_self ? `people may give ${terms().lower("clearance")} for their own runs` : `people may not give ${terms().lower("clearance")} for their own runs`}</b>{s && !s.sign_in ? " (sign-in is off)" : ""}.
          </p>
          {admin ? (
            <fieldset className="decide-form" style={{ border: 0, padding: 0, margin: 0 }} disabled={!obj}>
              <legend className="eyebrow">Choose</legend>
              {([["unset", "Use the default", "Yes without sign-in, no with sign-in."], ["no", "Someone else must give it", terms().say("The person who started a {run} cannot give it.")], ["yes", "People may give it for their own runs", "Faster for small teams, with no second look."]] as [Choice, string, string][]).map(([v, t, d]) => (
                <div key={v} style={{ display: "flex", gap: 10, alignItems: "flex-start" }}>
                  <input type="radio" id={`allow-${v}`} name="allow-self" checked={choice === v} onChange={() => { setChoice(v); setSaved(false); }} />
                  <label htmlFor={`allow-${v}`} style={{ fontWeight: 400, color: "var(--ink)" }}><b>{t}</b> <span className="faint">{d}</span></label>
                </div>
              ))}
              <ErrorBox error={error} />
              <div className="acts"><Button kind="pri" icon="check" disabled={busy || !obj} onClick={save}>Save</Button></div>
              {saved && <div className="note" role="status"><Icon name="check" />Saved. It applies within a few seconds and is in the history of the {terms().lower("installation")} settings.</div>}
            </fieldset>
          ) : (
            <div className="note"><Icon name="lock" />Only an administrator can change this. Ask one if you need to give {terms().lower("clearance")} {terms().say("for a {run} you started.")}</div>
          )}
        </div>
      </Sheet>
    </div>
  );
}
