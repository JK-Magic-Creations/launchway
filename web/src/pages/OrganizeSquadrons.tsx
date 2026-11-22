import { useCallback, useEffect, useMemo, useState } from "react";
import { ErrorBox } from "../components/ui";
import { announce } from "../kit/announce";
import { EmptyState, PageHead, Sheet } from "../kit/layout";
import { onLink } from "../router";
import { squadronsApi, type OrganizeProposal, type OrganizeResult, type SquadronList } from "../squadronsApi";
import { terms } from "../terms";
import { unitNameProblem } from "./squadrons/shared";
import "../moveflights.css";
import { BtnSm, DivLoading, DivPage, SpanFaintSmall } from "../kit/bits";

// Organize into Squadrons: for Flights that sit in no Squadron the server proposes groups (from folders people
// made and from name prefixes). The person renames a group, merges two, moves a Flight between groups or leaves
// it unfiled (all by buttons and selects, so the keyboard does everything), then applies: one audited change.

interface Group { id: number; name: string; display: string; flights: string[]; source: string }
const UNFILED = "unfiled";
const say = (t: string) => terms().say(t);
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

export function OrganizePage() {
  const [list, setList] = useState<SquadronList | null>(null);
  const [proposal, setProposal] = useState<OrganizeProposal | null>(null);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback(() => {
    Promise.all([squadronsApi.list(), squadronsApi.proposal()]).then(([l, p]) => { setList(l); setProposal(p); setError(null); }, setError);
  }, []);
  useEffect(load, [load]);
  const t = terms();
  const title = `Organize into ${t.say("{Units}")}`;

  if (error && !proposal) return <DivPage><ErrorBox error={error} /></DivPage>;
  if (!list || !proposal) return <DivPage><DivLoading>Loading…</DivLoading></DivPage>;
  return (
    <div className="page og">
      <PageHead eyebrow={t.say("{Units}")} title={title} sentence={say("Sort the {projects} that sit in no {unit} into named {units}. Nothing changes until you apply, and the whole change can be undone.")} />
      {!list.may_manage
        ? <Sheet><EmptyState title="You may not organize these"><span>{list.why || say("Ask someone who administers every {project}.")}</span></EmptyState></Sheet>
        : <Wizard proposal={proposal} existing={list.squadrons.map((s) => s.name)} onChanged={load} />}
    </div>
  );
}

function Wizard({ proposal, existing, onChanged }: { proposal: OrganizeProposal; existing: string[]; onChanged: () => void }) {
  const [groups, setGroups] = useState<Group[]>(() => (proposal.groups ?? []).map((g, i) => ({ id: i + 1, name: g.name, display: "", flights: [...g.flights], source: g.source })));
  const [left, setLeft] = useState<string[]>(() => [...(proposal.leftover ?? [])]);
  const [next, setNext] = useState((proposal.groups ?? []).length + 1);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<OrganizeResult | null>(null);
  const [undone, setUndone] = useState(false);
  const [status, setStatus] = useState("");
  const say2 = (s: string) => { setStatus(s); announce(s); };

  const problems = useMemo(() => {
    const out = new Map<number, string>();
    const seen = new Map<string, number>();
    for (const g of groups) {
      if (g.flights.length === 0) continue;
      const p = unitNameProblem(g.name);
      if (p) out.set(g.id, p);
      else if (seen.has(g.name.toLowerCase())) { out.set(g.id, `Two groups are named ${g.name}. Give each its own name, or merge them.`); out.set(seen.get(g.name.toLowerCase())!, `Two groups are named ${g.name}. Give each its own name, or merge them.`); }
      seen.set(g.name.toLowerCase(), g.id);
    }
    return out;
  }, [groups]);

  const filed = groups.filter((g) => g.flights.length > 0);
  const total = filed.reduce((n, g) => n + g.flights.length, 0);

  const rename = (id: number, name: string) => setGroups((gs) => gs.map((g) => (g.id === id ? { ...g, name } : g)));
  const display = (id: number, v: string) => setGroups((gs) => gs.map((g) => (g.id === id ? { ...g, display: v } : g)));
  const merge = (from: number, into: number) => {
    const a = groups.find((g) => g.id === from), b = groups.find((g) => g.id === into);
    if (!a || !b) return;
    setGroups((gs) => gs.filter((g) => g.id !== from).map((g) => (g.id === into ? { ...g, flights: [...g.flights, ...a.flights] } : g)));
    say2(say(`Merged ${a.name} into ${b.name}. ${plural(a.flights.length, "{project} moved", "{projects} moved")}.`));
  };
  // to is a group id, UNFILED, or "new" for a group of its own.
  const moveFlight = (flight: string, from: number | typeof UNFILED, to: string) => {
    const label = to === "new" ? flight : groups.find((g) => String(g.id) === to)?.name ?? "";
    setGroups((gs) => {
      const out = gs.map((g) => (g.id === from ? { ...g, flights: g.flights.filter((f) => f !== flight) } : g));
      if (to === "new") return [...out, { id: next, name: flight, display: "", flights: [flight], source: "you" }];
      return to === UNFILED ? out : out.map((g) => (String(g.id) === to ? { ...g, flights: [...g.flights, flight] } : g));
    });
    if (to === "new") setNext(next + 1);
    setLeft((l) => (from === UNFILED ? l.filter((f) => f !== flight) : l).concat(to === UNFILED && from !== UNFILED ? [flight] : []));
    say2(to === UNFILED ? `${flight} will stay unfiled.` : `${flight} will go to ${label || "its group"}.`);
  };

  const apply = async () => {
    setBusy(true); setErr("");
    try {
      const res = await squadronsApi.organize(filed.map((g) => ({ name: g.name, display_name: g.display.trim() || undefined, flights: g.flights })));
      setResult(res); setUndone(false);
      say2(say(`Filed ${plural(res.moved.length, "{project}", "{projects}")} in ${plural(filed.length, "{unit}", "{units}")} in one audited change.`) + " Undo is available.");
      onChanged();
    } catch (e) { setErr(e instanceof Error ? e.message : "The change was refused."); announce("The change was refused.", "assertive"); }
    setBusy(false);
  };
  const undo = async () => {
    if (!result) return;
    setBusy(true); setErr("");
    try { await squadronsApi.undoOrganize(result.undo, result.undo_delete ?? []); setUndone(true); say2(say("Undone: the {projects} are back where they were and the new groups are removed.")); onChanged(); }
    catch (e) { setErr(e instanceof Error ? e.message : "The undo was refused."); }
    setBusy(false);
  };

  if (result) {
    return (
      <Sheet title="Done">
        <div className="og">
          <p role="status">
            {undone ? say("Undone: the {projects} are back where they were and the groups this change made are removed.")
              : say(`Filed ${plural(result.moved.length, "{project}", "{projects}")} in one audited change${result.created.length ? `, and made ${plural(result.created.length, "{unit}", "{units}")}: ${result.created.join(", ")}` : ""}.`)}
            {!undone && <> <BtnSm disabled={busy} onClick={() => void undo()}>Undo</BtnSm></>}
          </p>
          {err && <p role="alert" className="err">{err}</p>}
          {!undone && (result.blocked?.length ?? 0) > 0 && (
            <div role="note" className="mv-note">
              <p><b>{say(`${plural(result.blocked.length, "{project} was", "{projects} were")} not moved`)}</b>, because a secret reference would stop working there:</p>
              <ul>{result.blocked.map((b) => <li key={b.flight}><b>{b.flight}</b>: {(b.reasons ?? []).join(" ")}</li>)}</ul>
            </div>
          )}
          <p><a className="btn" href="/units" onClick={onLink}>{say("Go to the {units}")}</a></p>
        </div>
      </Sheet>
    );
  }
  if (groups.every((g) => g.flights.length === 0) && left.length === 0 && (proposal.unfiled?.length ?? 0) === 0) {
    return <Sheet><EmptyState title={say("Every {project} is in a {unit}")}>Nothing to organize.</EmptyState></Sheet>;
  }
  const options = (cur: number | typeof UNFILED) => (
    <>
      {groups.filter((g) => g.id !== cur).map((g) => <option key={g.id} value={String(g.id)}>{g.name || "(no name)"}</option>)}
      {cur !== UNFILED && <option value={UNFILED}>Leave unfiled</option>}
      <option value="new">A new group of its own</option>
    </>
  );
  return (
    <div className="og">
      <p className="faint small">
        {(proposal.groups ?? []).length > 0 ? say(`These groups were proposed from the folders people made and from name prefixes. ${plural((proposal.unfiled ?? []).length, "{project} sits", "{projects} sit")} in no {unit}.`)
          : say(`No obvious groups were found for the ${plural((proposal.unfiled ?? []).length, "{project}", "{projects}")} in no {unit}. Make groups below, or leave them unfiled.`)}
      </p>
      <p role="status" className="small" aria-live="polite">{status}</p>
      <ul className="og-groups">
        {groups.map((g) => {
          const nameId = `og-name-${g.id}`;
          const exists = existing.includes(g.name);
          return (
            <li key={g.id} className="og-group">
              <h2 className="h-sm">{g.name || "(no name)"} <SpanFaintSmall>({plural(g.flights.length, say("{project}"), say("{projects}"))}{g.source === "folder" ? ", from a folder" : g.source === "prefix" ? ", from a shared name start" : ""})</SpanFaintSmall></h2>
              <label htmlFor={nameId}>{say("Name of the {unit}")}
                <input id={nameId} type="text" value={g.name} onChange={(e) => rename(g.id, e.target.value)} aria-invalid={problems.has(g.id)} aria-describedby={`${nameId}-p`} />
              </label>
              <p id={`${nameId}-p`} className={problems.has(g.id) ? "err small" : "faint small"} role={problems.has(g.id) ? "alert" : undefined}>
                {problems.get(g.id) ?? (exists ? say("This {unit} exists: these {projects} are added to it.") : say("A new {unit} will be made."))}
              </p>
              <label>Display name (optional)<input type="text" value={g.display} maxLength={100} onChange={(e) => display(g.id, e.target.value)} /></label>
              <ul className="og-flights" aria-label={`${g.name || "Group"}: ${say("{projects}")}`}>
                {g.flights.map((f) => (
                  <li key={f} className="og-flight">
                    <span>{f}</span>
                    <label>Move {f} to
                      <select value="" onChange={(e) => e.target.value && moveFlight(f, g.id, e.target.value)}><option value="">Choose…</option>{options(g.id)}</select>
                    </label>
                  </li>
                ))}
                {g.flights.length === 0 && <li className="faint small">Empty: it will not be made.</li>}
              </ul>
              {groups.length > 1 && (
                <div className="og-merge">
                  <label>Merge {g.name || "this group"} into
                    <select value="" onChange={(e) => e.target.value && merge(g.id, Number(e.target.value))}>
                      <option value="">Choose…</option>
                      {groups.filter((o) => o.id !== g.id).map((o) => <option key={o.id} value={o.id}>{o.name || "(no name)"}</option>)}
                    </select>
                  </label>
                </div>
              )}
            </li>
          );
        })}
      </ul>

      <Sheet title={say("{Projects} to leave unfiled")}>
        {left.length === 0 ? <p className="faint small">None: every {say("{project}")} is in a group.</p> : (
          <ul className="og-flights">
            {left.map((f) => (
              <li key={f} className="og-flight">
                <span>{f}</span>
                <label>Move {f} to
                  <select value="" onChange={(e) => e.target.value && moveFlight(f, UNFILED, e.target.value)}><option value="">Choose…</option>{options(UNFILED)}</select>
                </label>
              </li>
            ))}
          </ul>
        )}
      </Sheet>

      {err && <p role="alert" className="err">{err}</p>}
      <p className="small">
        {total === 0 ? "Nothing will be filed yet." : say(`Applying files ${plural(total, "{project}", "{projects}")} in ${plural(filed.length, "{unit}", "{units}")} as one audited change; ${left.length} stay${left.length === 1 ? "s" : ""} unfiled. A {project} whose secret references would stop working is left where it is and listed afterwards.`)}
      </p>
      <div className="og-actions">
        <button type="button" className="btn btn-primary" disabled={busy || total === 0 || problems.size > 0} onClick={() => void apply()}>{busy ? "Applying…" : "Apply"}</button>
        <a className="btn" href="/units" onClick={onLink}>Cancel</a>
      </div>
    </div>
  );
}
