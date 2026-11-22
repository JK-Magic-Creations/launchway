import { useCallback, useEffect, useState } from "react";
import { ErrorBox, Mono } from "../components/ui";
import { forgeApi, type HostedRepo, type RepoReach as Reach, type SshKey } from "../forgeApi";
import { mirrorsApi, type Mirror } from "../mirrors";
import { clock } from "../format";
import { parseRanges } from "./tokenLimits";

/**
 * "Who can reach this repository": people by role, deploy keys, tokens (service-account tokens
 * included) whose limits include it, and mirrors; and a one-step credential for this repository alone.
 */
export function RepoReach({ repo }: { repo: HostedRepo }) {
  const owner = repo.home ?? repo.path.split("/")[0], name = repo.name;
  const [reach, setReach] = useState<Reach | null>(null);
  const [keys, setKeys] = useState<SshKey[]>([]);
  const [mirrors, setMirrors] = useState<Mirror[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [tokenName, setTokenName] = useState("");
  const [access, setAccess] = useState<"read" | "write">("read");
  const [days, setDays] = useState(90);
  const [ranges, setRanges] = useState("");
  const [made, setMade] = useState<string | null>(null);
  const parsed = parseRanges(ranges);

  const load = useCallback(() => {
    forgeApi.reach(owner, name).then((r) => { setReach(r); setError(null); }).catch(setError);
    forgeApi.deployKeys(owner, name).then(setKeys).catch(() => setKeys([]));
    mirrorsApi.mirrors(repo.path).then(setMirrors).catch(() => setMirrors([]));
  }, [owner, name, repo.path]);
  useEffect(load, [load]);

  const make = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!parsed.ok) return;
    setError(null);
    try {
      const t = await forgeApi.makeCredential(owner, name, { name: tokenName.trim(), access, expires_in_days: days, source_ranges: parsed.values.length ? parsed.values : undefined });
      setMade(t.token); setTokenName(""); setRanges(""); load();
    } catch (err) { setError(err); }
  };

  return (
    <section aria-labelledby="reach-h">
      <h2 className="h-sm" id="reach-h">Who can reach this repository</h2>
      <ErrorBox error={error} />
      <h3 className="h-xs">People</h3>
      {reach && reach.people.length === 0 ? <p className="sub">No access bindings cover this repository.</p> : (
        <ul>{reach?.people.map((p, i) => <li key={i}>{p.who} <span className="faint">({p.kind}) can {p.role === "viewer" ? "read" : p.role === "operator" ? "read and push" : "administer"} it</span></li>)}</ul>
      )}
      <h3 className="h-xs">Deploy keys</h3>
      {keys.length === 0 ? <p className="sub">None.</p> : <ul>{keys.map((k) => <li key={k.id}>{k.name} <span className="faint">{k.can_write ? "read and push" : "read only"}, over SSH</span></li>)}</ul>}
      <h3 className="h-xs">Tokens limited to repositories that include this one</h3>
      {reach && reach.tokens.length === 0 ? <p className="sub">None.</p> : (
        <ul>{reach?.tokens.map((t) => (
          <li key={t.id}>{t.name} <span className="faint">of {t.owner}: {t.access === "write" ? "read and push" : "read only"} through <Mono>{t.pattern}</Mono>, expires {clock(t.expires_at)}{t.source_ranges?.length ? `, only from ${t.source_ranges.join(", ")}` : ""}</span></li>
        ))}</ul>
      )}
      <h3 className="h-xs">Mirrors</h3>
      {mirrors.length === 0 ? <p className="sub">None.</p> : <ul>{mirrors.map((m) => <li key={m.id}>{m.direction} mirror <span className="faint">{m.url}</span></li>)}</ul>}

      <h3 className="h-xs">Make a credential for this repository</h3>
      <p className="sub">A token over HTTPS Git for this repository alone, as a deploy key is over SSH. It belongs to a service account made for it, so it does not depend on a person.</p>
      <form className="inputs-form" aria-label="Make a credential for this repository" onSubmit={make}>
        <div className="tokenform-grid">
          <label className="field"><span>Name</span><input value={tokenName} required placeholder="for example: build system" onChange={(e) => setTokenName(e.target.value)} /></label>
          <label className="field"><span>Access</span>
            <select value={access} onChange={(e) => setAccess(e.target.value as "read" | "write")}><option value="read">Read only</option><option value="write">Read and push</option></select></label>
          <label className="field"><span>Expires after</span>
            <select value={days} onChange={(e) => setDays(Number(e.target.value))}>{[30, 90, 180, 365].map((d) => <option key={d} value={d}>{d} days</option>)}</select></label>
          <label className="field"><span>Only from addresses</span><input value={ranges} placeholder="anywhere, or: 203.0.113.0/24" onChange={(e) => setRanges(e.target.value)} /></label>
        </div>
        {!parsed.ok && <p className="bad-ink" role="alert">{parsed.problem}</p>}
        <div><button type="submit" className="btn btn-primary" disabled={!tokenName.trim() || !parsed.ok}>Make credential</button></div>
      </form>
      {made && (
        <div className="callout callout-ok tokenmade" role="status">
          <strong>The credential. Copy it now: it will not be shown again.</strong>
          <div className="tokenvalue"><input readOnly value={made} aria-label="The new credential" onFocus={(e) => e.currentTarget.select()} className="mono" /></div>
          <span>Use it as the password of HTTPS Git (any user name).</span>
          <button type="button" className="btn" onClick={() => setMade(null)}>Done</button>
        </div>
      )}
    </section>
  );
}
