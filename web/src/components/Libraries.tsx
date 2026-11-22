import { Fragment, useState } from "react";
import type { GraphGroup, GraphLibrary, LibraryOverrides } from "../api";
import { shortSha } from "../format";
import { Mono } from "./ui";
import { terms } from "../terms";
import { BtnSm, PSub } from "../kit/bits";

// LibraryList shows the libraries a pipeline used: name@version and the
// commit it resolved to, and any override asked for when the run started.
export function LibraryList({ libraries }: { libraries: GraphLibrary[] }) {
  return (
    <span className="libs">
      {libraries.map((l) => (
        <span key={l.name} className={"lib-chip" + (l.override ? " lib-chip-override" : "")}
          title={`${l.name} is the library ${l.repo}. ${l.version} ${l.override ? `was overridden by ${l.override}, which` : ""} ` +
            `${l.commit ? `pointed at ${l.commit}${l.ref_kind ? ` (a ${l.ref_kind})` : ""} when the run was created` : "is not resolved yet"}.`}>
          <Mono>{l.name}@{l.version}</Mono>
          {l.override && <span>overridden by <Mono>{l.override}</Mono></span>}
          {l.commit && <span aria-label={`commit ${l.commit}`}>→ <Mono>{shortSha(l.commit)}</Mono></span>}
        </span>
      ))}
    </span>
  );
}

// TemplateFacts lists, for a template's step, where it came from and the
// values its template was expanded with.
export function TemplateFacts({ group, libraries }: { group: GraphGroup; libraries?: GraphLibrary[] }) {
  const lib = libraries?.find((l) => l.name === group.library);
  const params = Object.entries(group.params ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return (
    <>
      <dt>Template</dt>
      <dd><Mono>{group.uses}</Mono>, used by <strong>{group.id}</strong>
        {lib?.commit && <> at <Mono>{shortSha(lib.commit)}</Mono></>}</dd>
      {params.length > 0 && (
        <>
          <dt>Parameters</dt>
          <dd>{params.map(([k, v]) => <div key={k}><Mono>{k}</Mono> = <Mono>{v}</Mono></div>)}</dd>
        </>
      )}
    </>
  );
}

// OverridesEditor edits library overrides: for a library the pipeline uses,
// a tag, branch or commit to use instead of the version it names.
export function OverridesEditor({ value, onChange, names }: {
  value: LibraryOverrides; onChange: (v: LibraryOverrides) => void; names?: string[];
}) {
  const [rows, setRows] = useState<[string, string][]>(() => {
    const r = Object.entries(value);
    return r.length ? r : [[names?.[0] ?? "", ""]];
  });
  const update = (next: [string, string][]) => {
    setRows(next);
    const out: LibraryOverrides = {};
    for (const [k, v] of next) if (k.trim() && v.trim()) out[k.trim()] = v.trim();
    onChange(out);
  };
  return (
    <div className="overrides">
      <PSub>{terms().say("Use another version of a library for this run only, to try a change before it is released: a branch, a tag or a full commit id. The {run} records both versions.")}</PSub>
      {names && names.length > 0 && <datalist id="override-names">{names.map((n) => <option key={n} value={n} />)}</datalist>}
      {rows.map(([k, v], i) => (
        <Fragment key={i}>
          <div className="override-row">
            <input aria-label="Library" placeholder="library, such as platform" value={k} list="override-names" maxLength={40}
              onChange={(e) => update(rows.map((r, j) => (j === i ? [e.target.value, r[1]] : r)))} />
            <input aria-label="Version to use" placeholder="branch, tag or commit" value={v} maxLength={128}
              onChange={(e) => update(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))} />
            <BtnSm onClick={() => update(rows.filter((_, j) => j !== i))}
              aria-label="Remove this override">Remove</BtnSm>
          </div>
        </Fragment>
      ))}
      <div><BtnSm onClick={() => setRows([...rows, ["", ""]])}>Add an override</BtnSm></div>
    </div>
  );
}
