import { useEffect, useState } from "react";
import { ApiError } from "../../api";
import { actionOf, guideApi, type GuideStep, type RootKeyCopyStatus } from "../../guideApi";
import { Unavailable, Wingman } from "./Frame";
import { prose } from "../../prose";
import { BtnPrimary, LabelField } from "../../kit/bits";
const P = await prose("pages.guide.EncryptionStep");

function download(name: string, data: unknown) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Step 4: what encryption protects, in plain words, and a guarded way to take a recovery copy of the root key. The
 * product records THAT a copy was taken and when, never the copy. */
export function EncryptionStep({ step, onChange }: { step: GuideStep; onChange: () => void }) {
  const take = actionOf(step, "download_root_key_copy");
  const [st, setSt] = useState<RootKeyCopyStatus | null>(null);
  const [open, setOpen] = useState(false);
  const [f, setF] = useState({ password: "", code: "", pass: "", pass2: "" });
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  useEffect(() => { guideApi.keyCopyStatus().then(setSt).catch(() => setSt(null)); }, [done]);

  const mismatch = f.pass2 !== "" && f.pass !== f.pass2;
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (mismatch) return;
    setBusy(true); setErr("");
    try {
      const file = await guideApi.takeKeyCopy({ current_password: f.password, code: f.code, passphrase: f.pass, passphrase_again: f.pass2 });
      download("launchway-root-key-copy.json", file);
      setF({ password: "", code: "", pass: "", pass2: "" });
      setDone(true); setOpen(false); onChange();
    } catch (x) { setErr(x instanceof ApiError ? x.message : P.t0); } finally { setBusy(false); }
  };
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: P.t1, slug: "security/hardening" }}>
        {P.t2}</Wingman>
      {step.encryption?.encrypted && (
        <dl className="gd-facts" aria-label="What is encrypted today">
          <dt>Root key held by</dt><dd>{step.encryption.root_key_holder?.kind.replace("_", " ")} {step.encryption.root_key_holder?.where ? `(${step.encryption.root_key_holder.where})` : ""}</dd>
          <dt>Stored secrets</dt><dd>{step.encryption.secrets_sealed} sealed, {step.encryption.secrets_remaining} not yet
            {step.encryption.migration.waiting_for_older_servers ? P.t3 : ""}</dd>
          <dt>Not encrypted yet</dt><dd>{step.encryption.not_yet_encrypted.join(", ")}</dd>
        </dl>
      )}
      {step.encryption?.warnings.map((w) => <div key={w} className="callout callout-draft">{w}</div>)}
      {st?.taken && <div className="callout callout-ok">A recovery copy was taken {st.taken_at ? new Date(st.taken_at).toLocaleString() : ""}{P.t4}</div>}
      <Unavailable action={take} />
      {take?.available && !open && <button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>{P.t5}</button>}
      {take?.available && open && (
        <form className="gd-form" onSubmit={submit} aria-label={P.t6}>
          <p className="faint">{P.t7}</p>
          <LabelField><span>Your password</span><input type="password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} autoComplete="current-password" required /></LabelField>
          <LabelField><span>{P.t8}</span><input value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} inputMode="numeric" autoComplete="one-time-code" required /></LabelField>
          <LabelField><span>{P.t9}</span><input type="password" value={f.pass} onChange={(e) => setF({ ...f, pass: e.target.value })} autoComplete="new-password" minLength={12} required /></LabelField>
          <LabelField><span>The passphrase again</span><input type="password" value={f.pass2} onChange={(e) => setF({ ...f, pass2: e.target.value })} autoComplete="new-password" required aria-invalid={mismatch} aria-describedby={mismatch ? "gd-pp-err" : undefined} /></LabelField>
          {mismatch && <p id="gd-pp-err" className="cf-problem" role="alert">{P.t10}</p>}
          {err && <div className="callout callout-bad" role="alert">{err}</div>}
          <div className="gd-choices"><BtnPrimary disabled={busy || mismatch}>{busy ? "Making the copy…" : "Download the copy"}</BtnPrimary>
            <button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button></div>
        </form>
      )}
      {done && <div className="callout callout-ok" role="status">{P.t11}</div>}
    </>
  );
}
