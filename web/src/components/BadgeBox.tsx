import { useEffect, useState } from "react";
import { call } from "../api";
import { announce } from "../kit/announce";
import { Sheet } from "../kit/layout";
import { ErrorBox } from "./ui";

interface Badges { enabled: boolean; public_repository: boolean; shown: boolean; markdown: Record<string, string>; links: Record<string, string> }

/** A Flight's badges: the Markdown to paste into a README, and (for an administrator of the Flight) the switch that shows
 * them to anyone. A Flight of a public repository needs no switch; for any other they are off until it is turned on. */
export function BadgeBox({ project, canSwitch }: { project: string; canSwitch: boolean }) {
  const [b, setB] = useState<Badges | null>(null);
  const [error, setError] = useState<unknown>(null);
  const path = `/api/v1/projects/${encodeURIComponent(project)}/badges`;
  useEffect(() => { call<Badges>("GET", path).then((v) => setB(v?.markdown ? v : null)).catch(setError); }, [path]);
  if (!b) return <ErrorBox error={error} />;
  const copy = (what: string, text: string) => {
    void navigator.clipboard?.writeText(text).then(() => announce(`The ${what} badge's Markdown copied.`), () => announce("It could not be copied; select the text and copy it.", "assertive"));
  };
  const flip = async () => { setError(null); try { setB(await call<Badges>("PUT", path, { enabled: !b.enabled })); } catch (e) { setError(e); } };
  return (
    <Sheet title="Badges" pad>
      <p className="sub">{b.shown
        ? (b.public_repository ? "Anyone can see these: the repository is public." : "Anyone can see these: they are switched on for this Flight.")
        : "These are off: until they are switched on they answer not found, so nothing about this Flight is shown."} A badge holds only a status word or a version.</p>
      {(["status", "release"] as const).map((k) => (
        <div key={k} className="field">
          <label htmlFor={`badge-${k}`}><span>{k === "status" ? "Status of the last Sortie on a branch" : "Latest release"}</span></label>
          <div className="row">
            <input id={`badge-${k}`} readOnly className="mono grow" value={b.markdown[k]} onFocus={(e) => e.currentTarget.select()} />
            <button type="button" className="btn btn-sm" onClick={() => copy(k, b.markdown[k])}>Copy badge<span className="visually-hidden"> for {k}</span></button>
          </div>
        </div>
      ))}
      <ErrorBox error={error} />
      {canSwitch && !b.public_repository && (
        <div><button type="button" className="btn btn-sm" onClick={() => void flip()}>{b.enabled ? "Hide the badges" : "Show the badges to anyone"}</button></div>
      )}
    </Sheet>
  );
}
