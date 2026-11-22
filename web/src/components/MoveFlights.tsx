import { useEffect, useMemo, useRef, useState } from "react";
import { announce } from "../kit/announce";
import { Dialog } from "../kit/Dialog";
import { squadronsApi, type MovePlan, type MovePreview, type MoveResult, type SquadronList } from "../squadronsApi";
import { terms } from "../terms";
import "../moveflights.css";

// Moving Flights between Squadrons. Three parts that work together:
//   useFlightSelection(names)  the ticked Flights among those in view (a ticked Flight that is not in view is not moved);
//   SelectAllFlights / FlightCheck  the checkboxes (a native checkbox: keyboard and screen reader complete);
//   MoveFlightsBar  the toolbar with "Move to Squadron…", the dialog (choose, then the preview of what changes,
//                   then apply) and, afterwards, the status sentence with Undo.
// To use it on a list of Flights (the Squadron page, an explorer): make a selection from the names in view, put a
// SelectAllFlights and a FlightCheck on each row, mount one MoveFlightsBar and reload the list in onMoved.

export interface FlightSelection {
  /** The ticked Flights that are in view, in the order of the view. */
  chosen: string[];
  has: (name: string) => boolean;
  toggle: (name: string) => void;
  setAll: (on: boolean) => void;
  clear: () => void;
  /** Whether all of the Flights in view are ticked, and whether some are. */
  allOn: boolean;
  someOn: boolean;
  inView: number;
}

export function useFlightSelection(names: string[]): FlightSelection {
  const [ticked, setTicked] = useState<Set<string>>(() => new Set());
  const key = names.join("\u0000");
  return useMemo(() => {
    const chosen = names.filter((n) => ticked.has(n));
    return {
      chosen, inView: names.length, has: (n) => ticked.has(n),
      toggle: (n) => setTicked((s) => { const x = new Set(s); if (x.has(n)) x.delete(n); else x.add(n); return x; }),
      setAll: (on) => setTicked((s) => { const x = new Set(s); for (const n of names) { if (on) x.add(n); else x.delete(n); } return x; }),
      clear: () => setTicked(new Set()),
      allOn: names.length > 0 && chosen.length === names.length, someOn: chosen.length > 0,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ticked, key]);
}

/** "Select all in view": ticked when all are, half-ticked when some are. */
export function SelectAllFlights({ selection, label }: { selection: FlightSelection; label?: string }) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (ref.current) ref.current.indeterminate = selection.someOn && !selection.allOn; }, [selection.someOn, selection.allOn]);
  return (
    <label className="mv-all">
      <input ref={ref} type="checkbox" checked={selection.allOn} disabled={selection.inView === 0} onChange={(e) => selection.setAll(e.target.checked)} />
      <span>{label ?? terms().say("Select all {projects} in view")}</span>
    </label>
  );
}

export function FlightCheck({ selection, name }: { selection: FlightSelection; name: string }) {
  return (
    <input type="checkbox" className="mv-check" checked={selection.has(name)} onChange={() => selection.toggle(name)}
      aria-label={terms().say(`Select {project} ${name}`)} />
  );
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
const say = (t: string) => terms().say(t);

/** The toolbar over a list of Flights, with the dialog and the result of the last move. */
export function MoveFlightsBar({ selection, onMoved }: { selection: FlightSelection; onMoved?: () => void }) {
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState<{ moved: string[]; undo: Record<string, string>; text: string } | null>(null);
  const [undoErr, setUndoErr] = useState("");
  const opener = useRef<HTMLButtonElement>(null);
  const n = selection.chosen.length;

  const undo = async () => {
    if (!done) return;
    try {
      await squadronsApi.move(done.moved, "", "Undo of a move", done.undo);
      announce(say(`Put ${plural(done.moved.length, "{project}", "{projects}")} back where they were.`));
      setDone(null); setUndoErr("");
      onMoved?.();
    } catch (e) { setUndoErr(e instanceof Error ? e.message : "The undo was refused."); announce("The undo was refused.", "assertive"); }
  };

  return (
    <div className="mv-bar">
      <div role="toolbar" aria-label={say("Selected {projects}")} className="mv-tools">
        <SelectAllFlights selection={selection} />
        <span className="small faint" role="status">{n === 0 ? say("No {projects} selected.") : say(`${plural(n, "{project}", "{projects}")} selected.`)}</span>
        <button ref={opener} type="button" className="btn btn-sm" disabled={n === 0} onClick={() => setOpen(true)}>{say("Move to {Unit}…")}</button>
      </div>
      {done && (
        <p className="mv-done" role="status">
          {done.text} <button type="button" className="btn btn-sm" onClick={() => void undo()}>Undo</button>
          {undoErr && <span className="err" role="alert"> {undoErr}</span>}
        </p>
      )}
      {open && (
        <MoveFlightsDialog flights={selection.chosen} onClose={() => { setOpen(false); opener.current?.focus(); }}
          onApplied={(res, toLabel) => {
            setOpen(false);
            const moved = res.moved.length;
            const text = say(`Moved ${plural(moved, "{project}", "{projects}")} to ${toLabel} in one audited change.`) + (res.blocked.length ? say(` ${plural(res.blocked.length, "{project} was", "{projects} were")} left where ${res.blocked.length === 1 ? "it was" : "they were"}.`) : "");
            setDone({ moved: res.moved, undo: res.undo, text });
            announce(text + " Undo is available.");
            selection.clear();
            onMoved?.();
          }} />
      )}
    </div>
  );
}

/** The dialog: choose the Squadron, read what would change for each Flight, apply the ones that can move. */
export function MoveFlightsDialog({ flights, onClose, onApplied }: { flights: string[]; onClose: () => void; onApplied: (res: MoveResult, toLabel: string) => void }) {
  const [list, setList] = useState<SquadronList | null>(null);
  const [err, setErr] = useState("");
  const [to, setTo] = useState<string | null>(null);
  const [comment, setComment] = useState("");
  const [preview, setPreview] = useState<MovePreview | null>(null);
  const [skip, setSkip] = useState<Set<string>>(() => new Set());
  const [busy, setBusy] = useState(false);

  useEffect(() => { squadronsApi.list().then(setList).catch((e) => setErr(e instanceof Error ? e.message : say("The {units} could not be loaded."))); }, []);
  const toLabel = to === null ? "" : to === "" ? "Unfiled" : (list?.squadrons.find((s) => s.name === to)?.display_name || to);

  const look = async () => {
    if (to === null) return;
    setBusy(true); setErr("");
    try { setPreview(await squadronsApi.previewMove(flights, to)); setSkip(new Set()); announce("Preview ready."); }
    catch (e) { setErr(e instanceof Error ? e.message : "The preview was refused."); }
    setBusy(false);
  };
  const movable = (preview?.plans ?? []).filter((p) => !p.blocked && !p.unchanged && !skip.has(p.flight));
  const apply = async () => {
    if (to === null) return;
    setBusy(true); setErr("");
    try { onApplied(await squadronsApi.move(movable.map((p) => p.flight), to, comment.trim() || undefined), toLabel); return; }
    catch (e) { setErr(e instanceof Error ? e.message : "The move was refused."); announce("The move was refused.", "assertive"); }
    setBusy(false);
  };

  return (
    <Dialog label={say(`Move ${plural(flights.length, "{project}", "{projects}")} to a {Unit}`)} onClose={onClose} className="mv-modal">
      <div className="mv-dialog">
        <h2 className="h-sm">{say(`Move ${plural(flights.length, "{project}", "{projects}")} to a {Unit}`)}</h2>
        <p className="faint small">{flights.join(", ")}</p>
        {!preview && (
          <>
            {list && !list.may_manage && <p className="mv-note" role="note">{list.why || say("You may not move {projects}.")}</p>}
            <fieldset className="mv-dest" disabled={!list}>
              <legend>{say("Move to which {unit}?")}</legend>
              {!list && !err && <p className="faint" role="status">Loading…</p>}
              {list && (
                <>
                  <label className="mv-opt"><input type="radio" name="mv-to" data-autofocus checked={to === ""} onChange={() => setTo("")} /> <span>Unfiled <span className="faint small">(in no {say("{unit}")})</span></span></label>
                  {list.squadrons.map((s) => (
                    <label key={s.name} className="mv-opt"><input type="radio" name="mv-to" checked={to === s.name} onChange={() => setTo(s.name)} /> <span>{s.display_name || s.name} <span className="faint small">({plural(s.flights, say("{project}"), say("{projects}"))})</span></span></label>
                  ))}
                  {list.squadrons.length === 0 && <p className="faint small">{say("There is no {unit} yet. Make one first, or use Organize.")}</p>}
                </>
              )}
            </fieldset>
          </>
        )}
        {preview && <Preview preview={preview} toLabel={toLabel} skip={skip} onSkip={(f) => setSkip((s) => { const x = new Set(s); if (x.has(f)) x.delete(f); else x.add(f); return x; })} />}
        {preview && <label className="mv-comment">Why (kept in the audit log, optional)<input value={comment} maxLength={200} onChange={(e) => setComment(e.target.value)} /></label>}
        {err && <p role="alert" className="err">{err}</p>}
        <div className="xp-dialog-f">
          <button type="button" className="btn" onClick={preview ? () => setPreview(null) : onClose}>{preview ? "Back" : "Cancel"}</button>
          {!preview
            ? <button type="button" className="btn btn-primary" disabled={to === null || busy || !list?.may_manage} onClick={() => void look()}>{busy ? "Looking…" : "See what would change"}</button>
            : <button type="button" className="btn btn-primary" disabled={movable.length === 0 || busy} onClick={() => void apply()}>{busy ? "Moving…" : say(`Move ${plural(movable.length, "{project}", "{projects}")}`)}</button>}
        </div>
      </div>
    </Dialog>
  );
}

function Preview({ preview, toLabel, skip, onSkip }: { preview: MovePreview; toLabel: string; skip: Set<string>; onSkip: (f: string) => void }) {
  const blocked = preview.plans.filter((p) => p.blocked).length;
  return (
    <div className="mv-preview">
      <p role="status" className="small">
        {say(`Moving to ${toLabel}. ${plural(preview.plans.length - blocked, "{project} can", "{projects} can")} move`)}{blocked > 0 ? say(`; ${plural(blocked, "{project} is", "{projects} are")} blocked and stays where ${blocked === 1 ? "it is" : "they are"}`) : ""}.
        {" "}Nothing has changed yet.
      </p>
      <ul className="mv-plans">
        {preview.plans.map((p) => <li key={p.flight}><PlanView plan={p} toLabel={toLabel} skipped={skip.has(p.flight)} onSkip={() => onSkip(p.flight)} /></li>)}
      </ul>
    </div>
  );
}

const SECRET: Record<MovePlan["secrets"][number]["result"], string> = {
  same: "resolves to the same store, so it keeps working",
  different: "resolves to a different store, so it would stop finding its value",
  none: "would resolve to no store at all",
};
const place = (s: string) => s || "Unfiled";

function PlanView({ plan, toLabel, skipped, onSkip }: { plan: MovePlan; toLabel: string; skipped: boolean; onSkip: () => void }) {
  const id = `mv-${plan.flight.replace(/[^a-z0-9]/gi, "-")}`;
  const nothing = !plan.stops?.length && !plan.starts?.length && !plan.variables?.length && !plan.access?.length && !plan.secrets?.length;
  return (
    <section className={"mv-plan" + (plan.blocked ? " blocked" : "")} aria-labelledby={id}>
      <h3 id={id} className="mv-plan-h">
        <label className="mv-tick">
          <input type="checkbox" checked={!plan.blocked && !plan.unchanged && !skipped} disabled={plan.blocked || !!plan.unchanged} onChange={onSkip} aria-describedby={`${id}-s`} />
          <span>{plan.flight}</span>
        </label>
        <span className="faint small"> {place(plan.from)} → {toLabel}</span>
      </h3>
      <p id={`${id}-s`} className="small mv-state">
        {plan.blocked ? "Blocked: it cannot move." : plan.unchanged ? `Already in ${toLabel}: nothing to do.` : skipped ? "Left out: it will stay where it is." : "Ready to move."}
      </p>
      {plan.blocked && (
        <div className="mv-blocked" role="note">
          <ul>{(plan.reasons ?? []).map((r, i) => <li key={i}>{r}</li>)}</ul>
          <p className="small"><b>What to do:</b> make each secret reference the same in both places (or point it at a store both {say("{units}")} use), then move it again. The other {say("{projects}")} can still move now.</p>
        </div>
      )}
      {!plan.unchanged && (
        <dl className="mv-effects">
          {plan.references_from === "assumed" && <><dt>Secrets</dt><dd>The pipeline file could not be read, so the secret references below are assumed.</dd></>}
          {(plan.stops?.length ?? 0) > 0 && <><dt>Stop applying</dt><dd><ul>{plan.stops.map((e, i) => <li key={i}>{e.kind} {e.name} <span className="faint">from {e.from || "its place"}{e.note ? `, ${e.note}` : ""}</span></li>)}</ul></dd></>}
          {(plan.starts?.length ?? 0) > 0 && <><dt>Start applying</dt><dd><ul>{plan.starts.map((e, i) => <li key={i}>{e.kind} {e.name} <span className="faint">from {e.from || "its place"}{e.note ? `, ${e.note}` : ""}</span></li>)}</ul></dd></>}
          {(plan.variables?.length ?? 0) > 0 && <><dt>Variables that change value</dt><dd><ul>{plan.variables.map((v) => <li key={v.name}><code>{v.name}</code>: {v.before || "(not set)"} becomes {v.after || "(not set)"}</li>)}</ul></dd></>}
          {(plan.access?.length ?? 0) > 0 && <><dt>Who can start runs</dt><dd><ul>{plan.access.map((a, i) => <li key={i}>{a.who} {a.effect === "gains" ? "gains" : "loses"} the ability to start runs ({a.before || "no role"} becomes {a.after || "no role"})</li>)}</ul></dd></>}
          {(plan.secrets?.length ?? 0) > 0 && <><dt>Secret references</dt><dd><ul>{plan.secrets.map((s, i) => <li key={i}><b>{s.result}</b>: {s.store} {SECRET[s.result]}</li>)}</ul></dd></>}
          {nothing && <><dt>Changes</dt><dd>No shared setting, variable or access changes.</dd></>}
        </dl>
      )}
    </section>
  );
}
