import { useState } from "react";
import { argoLink, normalizeArgoUrl, saveArgoUrl, storedArgoUrl } from "../guided/argo";
import { Icon } from "../kit/Icon";

/** The address of the Argo CD console, kept in this browser. */
export function useArgoUrl(): [string, (u: string) => void] {
  const [url, setUrl] = useState(storedArgoUrl);
  return [url, (u) => setUrl(saveArgoUrl(u))];
}

/** OpenInArgo is the prominent way into the engine's own console for an application (or one of its resources).
 * Until the console's address is known it asks for it once; the address stays in this browser. */
export function OpenInArgo({ application, resource, sm, kind }: {
  application: string; resource?: Parameters<typeof argoLink>[2]; sm?: boolean; kind?: "primary";
}) {
  const [base, setBase] = useArgoUrl();
  const [asking, setAsking] = useState(false);
  const [text, setText] = useState("");
  const cls = "btn" + (kind === "primary" ? " btn-primary" : "") + (sm ? " btn-sm" : "");
  if (base) {
    return <a className={cls} href={argoLink(base, application, resource)} target="_blank" rel="noreferrer noopener" data-argo="open">
      <Icon name="external" size={sm ? "s" : undefined} />Open in Argo CD</a>;
  }
  if (!asking) return <button type="button" className={cls} onClick={() => setAsking(true)} data-argo="ask"><Icon name="external" size={sm ? "s" : undefined} />Open in Argo CD</button>;
  const ok = normalizeArgoUrl(text) !== "";
  return (
    <form className="row wrap" onSubmit={(e) => { e.preventDefault(); if (ok) { setBase(text); setAsking(false); } }}>
      <label className="visually-hidden" htmlFor="argo-url">Address of Argo CD</label>
      <input id="argo-url" type="url" placeholder="https://argocd.example.com" value={text} onChange={(e) => setText(e.target.value)} ref={(el) => el?.focus()} />
      <button type="submit" className="btn btn-primary btn-sm" disabled={!ok}>Save and open</button>
      <button type="button" className="btn btn-sm" onClick={() => setAsking(false)}>Cancel</button>
      <span className="faint small">Kept in this browser only.</span>
    </form>
  );
}
