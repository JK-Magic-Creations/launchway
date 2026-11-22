import { useEffect, useMemo, useState } from "react";
import { ApiError } from "../../api";
import { Empty, ErrorBox } from "../../components/ui";
import { TextInput } from "../../components/ConfigForm";
import { announce } from "../../kit/announce";
import { Button, EmptyState, LinkButton, PageHead, Sheet } from "../../kit/layout";
import { Chip } from "../../kit/Status";
import { onLink } from "../../router";
import { squadronsApi, type AddedFlight, type Candidate, type Candidates, type SquadronPage } from "../../squadronsApi";
import { terms } from "../../terms";
import { setUrl, useUrlState, str } from "../../urlState";
import { useLoad } from "../../guided/useLoad";
import { useCallback } from "react";
import { flightNameFrom, flightNameProblem, SQUADRONS, squadronHref, WhyNot } from "./shared";
import "../../squadrons.css";
import "../../config.css";
import { prose } from "../../prose";
import { DivLoading, DivPage, SpanFaint } from "../../kit/bits";
const P = await prose("pages.squadrons.AddFlight");

// Add a Flight: find a repository (hosted ones without a Flight first), say what to call it, and see what pipeline it runs.
// Every step is an address: ?q= the search, ?connection= a connection searched on request, ?pick= the chosen repository
// (with ?from=hosted for a repository Launchway hosts), ?added= the Flight just made.

const here = (name: string, params: Record<string, string>) => {
  const qs = new URLSearchParams(params).toString();
  return `/units/${encodeURIComponent(name)}/projects/new${qs ? "?" + qs : ""}`;
};

export function AddFlightPage({ unit }: { unit: string }) {
  const fn = useCallback(() => squadronsApi.show(unit), [unit]);
  const { data, error } = useLoad<SquadronPage>(fn);
  const [pick] = useUrlState("pick", "", str);
  const [added] = useUrlState("added", "", str);
  const [result, setResult] = useState<AddedFlight | null>(null);
  const t = terms();

  if (error && !data) {
    if (error instanceof ApiError && error.status === 404) {
      return <div className="page sq-page"><Empty title={t.say("There is no {unit} called " + unit)}><a href={SQUADRONS} onClick={onLink}>{t.say("See every {unit}")}</a> or <a href="/units/new" onClick={onLink}>{t.say("add a {unit}")}</a>.</Empty></div>;
    }
    return <div className="page sq-page"><ErrorBox error={error} /></div>;
  }
  if (!data) return <DivPage><DivLoading>Loading…</DivLoading></DivPage>;
  const title = data.display_name || data.name;
  const head = <PageHead eyebrow={<a href={squadronHref(unit)} onClick={onLink}>{title}</a>} title={t.say("Add a {project}")}
    sentence={t.say(P.t0) + title + "."} />;
  if (!data.may_manage) return <div className="page sq-page">{head}<Sheet><WhyNot why={data.why ?? ""} /></Sheet></div>;
  if (added) return <div className="page sq-page">{head}<Added unit={unit} name={added} result={result?.name === added ? result : null} /></div>;
  if (pick) return <div className="page sq-page">{head}<Confirm unit={unit} onAdded={(r) => { setResult(r); announce(t.say(`The {project} ${r.name} was added.`)); setUrl({ pick: null, from: null, added: r.name }, "push"); }} /></div>;
  return <div className="page sq-page">{head}<Choose unit={unit} /></div>;
}

/** Step 1: search the repositories the person may see. */
function Choose({ unit }: { unit: string }) {
  const [q] = useUrlState("q", "", str);
  const [connection] = useUrlState("connection", "", str);
  const [typed, setTyped] = useState(q);
  const [state, setState] = useState<{ data: Candidates | null; error: unknown; loading: boolean }>({ data: null, error: null, loading: true });
  const [address, setAddress] = useState("");
  const t = terms();

  useEffect(() => {
    let live = true;
    const id = window.setTimeout(() => {
      setState((s) => ({ ...s, loading: true }));
      squadronsApi.candidates(q, connection || undefined)
        .then((data) => live && setState({ data, error: null, loading: false }))
        .catch((error) => live && setState((s) => ({ data: s.data, error, loading: false })));
    }, state.data ? 250 : 0);
    return () => { live = false; window.clearTimeout(id); };
  }, [q, connection]); // eslint-disable-line react-hooks/exhaustive-deps

  const d = state.data;
  const hosted = useMemo(() => d?.hosted ?? [], [d]);
  const fresh = useMemo(() => hosted.filter((c) => !c.flight), [hosted]);
  const taken = useMemo(() => hosted.filter((c) => c.flight), [hosted]);
  const connections = d?.connections ?? [];
  const nothing = d !== null && hosted.length === 0 && connections.length === 0 && q === "";
  const pickHref = (c: Candidate) => here(unit, c.source === "hosted" ? { pick: c.path, from: "hosted" } : { pick: c.address, from: "address" });

  return (
    <>
      <Sheet title="Find the repository" pad>
        <form role="search" onSubmit={(e) => { e.preventDefault(); setUrl({ q: typed.trim() || null }, "replace"); }}>
          <TextInput label="Search repositories" focusOnMount value={typed} onChange={(v) => { setTyped(v); setUrl({ q: v.trim() || null }, "replace"); }} placeholder="payments"
            help={t.say(P.t1)} />
        </form>
        <p className="faint small" role="status" aria-live="polite">
          {state.loading && !d ? "Looking…" : d ? `${hosted.length} ${hosted.length === 1 ? "repository" : "repositories"} found${fresh.length ? `, ${d.hosted_without_flight} without a ${t.lower("project")}` : ""}.` : ""}
        </p>
      </Sheet>
      {state.error ? <ErrorBox error={state.error} /> : null}
      {nothing && (
        <Sheet><EmptyState title={P.t2}>
          {t.say(P.t3)}
          <span className="sq-links"> <a href="/repositories" onClick={onLink}>Hosted repositories</a> · <a href={P.t4} onClick={onLink}>Connect a repository</a></span>
        </EmptyState></Sheet>
      )}
      {fresh.length > 0 && <CandidateList title={t.say(`Without a {project} yet (${d?.hosted_without_flight ?? fresh.length})`)} items={fresh} href={pickHref} />}
      {taken.length > 0 && <CandidateList title={t.say("Already used by a {project}")} items={taken} href={pickHref} />}
      {d && hosted.length === 0 && !nothing && <p className="faint" role="status">{`No hosted repository matches "${q}".`}</p>}
      {connections.length > 0 && (
        <Sheet title="Repositories on a connection">
          <p className="sh-b faint">{P.t5}</p>
          <div className="sq-conns">
            {connections.map((c) => (
              <a key={c} href={here(unit, { ...(q ? { q } : {}), ...(connection === c ? {} : { connection: c }) })} onClick={onLink} className="btn btn-sm">
                {connection === c ? `Hide ${c}` : `Search ${c}`}
              </a>
            ))}
          </div>
          {connection && d?.connection_error && <p className="callout sq-note" role="alert">{d.connection_error}</p>}
          {connection && !d?.connection_error && (d?.connection_items.length ?? 0) === 0 && !state.loading && <p className="sh-b faint" role="status">{`${connection} shows no repository${q ? " matching the search" : ""}.`}</p>}
        </Sheet>
      )}
      {connection && (d?.connection_items.length ?? 0) > 0 && <CandidateList title={`Through ${connection}`} items={d!.connection_items} href={pickHref} />}
      <Sheet title="I have a plain Git address" pad>
        <form onSubmit={(e) => { e.preventDefault(); const a = address.trim(); if (a) setUrl({ pick: a, from: "address", q: null, connection: null }, "push"); }}>
          <TextInput label="Git address" mono value={address} onChange={setAddress} placeholder={P.t6} help={P.t7} />
          <Button kind="pri" type="submit" disabled={address.trim() === ""}>Continue</Button>
        </form>
      </Sheet>
    </>
  );
}

function CandidateList({ title, items, href }: { title: string; items: Candidate[]; href: (c: Candidate) => string }) {
  return (
    <Sheet title={title}>
      <ul className="rowlist">
        {items.map((c) => (
          <li key={c.source + c.path}>
            <div className="main">
              <div className="nm mono">{c.path}</div>
              <div className="sb faint">{[c.source === "hosted" ? "Hosted by Launchway" : `On ${c.source}`, c.visibility, c.default_branch ? `default branch ${c.default_branch}` : ""].filter(Boolean).join(" · ")}</div>
            </div>
            <div className="cell">{c.flight && <Chip tone="warn">{terms().say("already a {project}: ")}{c.flight}</Chip>}</div>
            <a href={href(c)} onClick={onLink} className="btn btn-sm" aria-label={`Choose ${c.path}`}>Choose</a>
          </li>
        ))}
      </ul>
    </Sheet>
  );
}

/** Step 2: the name and the branch, then make it. */
function Confirm({ unit, onAdded }: { unit: string; onAdded: (r: AddedFlight) => void }) {
  const [pick] = useUrlState("pick", "", str);
  const [from] = useUrlState("from", "address", str);
  const hosted = from === "hosted";
  const [name, setName] = useState(() => flightNameFrom(pick));
  const [branch, setBranch] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [touched, setTouched] = useState(false);
  const t = terms();
  const problem = !touched && name === "" ? "" : flightNameProblem(name);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (flightNameProblem(name)) return;
    setBusy(true); setError(null);
    try {
      onAdded(await squadronsApi.addFlight(unit, { ...(hosted ? { repository: pick } : { repo: pick }), name, ...(branch.trim() ? { branch: branch.trim() } : {}) }));
    } catch (err) { setError(err); announce(err instanceof Error ? err.message : "That did not work.", "assertive"); setBusy(false); }
  };
  return (
    <Sheet title="Check and add" pad>
      <form className="sq-form" onSubmit={submit} noValidate aria-label={t.say("Add a {project}")}>
        <p className="sq-pick"><SpanFaint>Repository</SpanFaint> <b className="mono">{pick}</b> <a href={here(unit, {})} onClick={onLink}>Choose another</a></p>
        <TextInput label={t.say("Name of the {project}")} required focusOnMount mono value={name} onChange={setName} problems={problem ? [problem] : undefined} help={P.t8} />
        <TextInput label="Branch (optional)" mono value={branch} onChange={setBranch} placeholder={P.t9} help={P.t10} />
        <ErrorBox error={error} />
        <div className="sq-actions">
          <Button kind="pri" type="submit" disabled={busy} aria-busy={busy || undefined}>{busy ? "Adding…" : t.say("Add the {project}")}</Button>
          <LinkButton href={here(unit, {})}>Cancel</LinkButton>
        </div>
      </form>
    </Sheet>
  );
}

/** Step 3: what pipeline the new Flight runs. */
function Added({ unit, name, result }: { unit: string; name: string; result: AddedFlight | null }) {
  const [copied, setCopied] = useState(false);
  const t = terms();
  const p = result?.pipeline;
  const copy = async () => {
    try { await navigator.clipboard.writeText(p?.starter ?? ""); setCopied(true); announce(P.t11); }
    catch { announce(P.t12, "assertive"); }
  };
  return (
    <>
      <Sheet pad>
        <div role="status"><h2 className="h-sm">{t.say(`The {project} ${name} was added.`)}</h2></div>
        {!p && <p>{t.say(P.t13)}</p>}
        {p?.state === "found" && <p>{p.note} {p.path && <>The pipeline file is <code>{p.path}</code>.</>}</p>}
        {p?.state === "starter" && (
          <>
            <p>{p.note}</p>
            {/* A scrollable region must be reachable by keyboard. */}
            {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
            <pre className="sq-starter" tabIndex={0} aria-label="A starter pipeline, read only">{p.starter}</pre>
            <div className="sq-actions">
              <Button onClick={copy}>{copied ? "Copied" : "Copy the starter"}</Button>
              <LinkButton href={`/projects/${encodeURIComponent(name)}/pipeline/edit`} kind="pri">Open the pipeline editor</LinkButton>
              <LinkButton href={`/projects/${encodeURIComponent(name)}/import`}>Import an existing pipeline</LinkButton>
            </div>
          </>
        )}
        <div className="sq-actions">
          <LinkButton href={`/projects/${encodeURIComponent(name)}/pipeline`}>{t.say("Open the {project}")}</LinkButton>
          <LinkButton href={here(unit, {})}>{t.say("Add another {project}")}</LinkButton>
          <LinkButton href={squadronHref(unit)}>{t.say("Back to the {unit}")}</LinkButton>
        </div>
      </Sheet>
    </>
  );
}
