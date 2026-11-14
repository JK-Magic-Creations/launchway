import { useEffect, useMemo, useState } from "react";
import { api, type Ref } from "../api";
import { ago } from "../format";
import { ErrorBox } from "./ui";

interface Props {
  project: string;
  defaultBranch: string;
  /** The name of the chosen branch or tag. */
  chosen: string;
  onChoose: (r: Ref) => void;
  /** List branches only, for choosing what to edit. */
  branchesOnly?: boolean;
}

// RefPicker lists a project's branches and tags, read from the repository
// as it opens, with a search box. The default branch is chosen first.
export function RefPicker({ project, defaultBranch, chosen, onChoose, branchesOnly }: Props) {
  const [refs, setRefs] = useState<Ref[] | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [query, setQuery] = useState("");

  useEffect(() => {
    api.refs(project).then((all) => {
      const r = branchesOnly ? all.filter((x) => x.kind === "branch") : all;
      setRefs(r);
      if (!chosen) {
        const first = r.find((x) => x.name === defaultBranch) ?? r[0];
        if (first) onChoose(first);
      }
    }).catch(setLoadErr);
    // Read once per opening; choosing does not read the repository again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project, branchesOnly]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (refs ?? []).filter((r) => !q || r.name.toLowerCase().includes(q) || r.subject.toLowerCase().includes(q)).slice(0, 50);
  }, [refs, query]);

  return (
    <>
      <label className="field">
        <span>{branchesOnly ? "Branch" : "Branch or tag"}</span>
        <input type="search" placeholder={branchesOnly ? "Search branches and commit messages" : "Search branches, tags and commit messages"}
          value={query} onChange={(e) => setQuery(e.target.value)} autoFocus />
      </label>
      <ErrorBox error={loadErr} />
      {refs === null && !loadErr && <div className="loading">Reading the repository…</div>}
      <div className="reflist" role="listbox" aria-label={branchesOnly ? "Branches" : "Branches and tags"}>
        {shown.map((r) => (
          <button key={r.kind + r.name} type="button" role="option" aria-selected={chosen === r.name}
            className={"refrow" + (chosen === r.name ? " refrow-on" : "")} onClick={() => onChoose(r)}>
            <span className={"refkind refkind-" + r.kind}>{r.kind}</span>
            <span className="refname mono">{r.name}</span>
            <span className="refsubj">{r.subject}</span>
            <span className="faint small">{r.author} · {ago(r.committed_at)}</span>
          </button>
        ))}
        {refs && shown.length === 0 && <div className="sub">Nothing matches.</div>}
      </div>
    </>
  );
}
