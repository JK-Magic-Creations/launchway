import { useEffect, useState } from "react";
import { ErrorBox, Mono } from "../components/ui";
import { Sheet } from "../kit/layout";
import { forgeApi, type HostedRepo, type TagRule } from "../forgeApi";

const blank = (pattern: string): TagRule => ({ pattern, create_role: "admin", change_role: "", pipelines: [] });

/** The protected tags of a hosted repository, next to its protected branches: which tags are covered, who may make
 * one, who (by default nobody) may move or delete one, and which Flights may make one with their own credential.
 * The rules are enforced for Git over HTTPS and SSH, for the API and when a release makes its tag. */
export function ProtectedTags({ repo }: { repo: HostedRepo }) {
  const [rules, setRules] = useState<TagRule[] | null>(null);
  const [saved, setSaved] = useState(false);
  const [adding, setAdding] = useState("");
  const [texts, setTexts] = useState<Record<string, string>>({}); // what is typed in a Flights field, before it is split
  const [error, setError] = useState<unknown>(null);
  const owner = repo.home ?? "";
  useEffect(() => { forgeApi.protectedTags(owner, repo.name).then(setRules).catch(setError); }, [owner, repo.name]);
  if (rules === null) return <ErrorBox error={error} />;
  const patch = (i: number, p: Partial<TagRule>) => { setRules(rules.map((r, j) => (j === i ? { ...r, ...p } : r))); setSaved(false); };
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null); setSaved(false);
    try { setRules(await forgeApi.setProtectedTags(owner, repo.name, rules)); setSaved(true); } catch (err) { setError(err); }
  };
  return (
    <Sheet title="Protected tags" pad>
      <p className="sub">A tag that matches a pattern such as <Mono>v*</Mono> is protected: by default only an administrator of this repository may make one, and nobody may move or delete it, so a version people have seen stays what it was. The rules hold for Git over HTTPS and SSH, for the API and for the tag a release makes.</p>
      <form onSubmit={save} className="inputs-form" aria-label="Protected tags">
        {rules.length === 0 && <p className="faint">No tag is protected.</p>}
        {rules.map((r, i) => (
          <fieldset key={r.pattern} className="card inputs-form">
            <legend><Mono>{r.pattern}</Mono></legend>
            <label className="field"><span>Who may make a matching tag</span>
              <select value={r.create_role} onChange={(e) => patch(i, { create_role: e.target.value })}>
                <option value="admin">Administrators of the repository</option>
                <option value="operator">Anyone who can push to the repository</option>
              </select></label>
            <label className="field"><span>Who may move or delete one</span>
              <select value={r.change_role} onChange={(e) => patch(i, { change_role: e.target.value })}>
                <option value="">Nobody</option>
                <option value="admin">Administrators of the repository</option>
              </select></label>
            <label className="field"><span>Flights that may make one</span>
              <input value={texts[r.pattern] ?? r.pipelines.join(", ")} placeholder="ship-*, release"
                onChange={(e) => { setTexts({ ...texts, [r.pattern]: e.target.value }); patch(i, { pipelines: e.target.value.split(",").map((x) => x.trim()).filter(Boolean) }); }} />
              <small>Flight names or patterns whose credential may make a matching tag. They can never move or delete one.</small></label>
            <div><button type="button" className="btn btn-danger-outline btn-sm" onClick={() => { setRules(rules.filter((_, j) => j !== i)); setSaved(false); }}>Remove the rule for {r.pattern}</button></div>
          </fieldset>
        ))}
        <label className="field"><span>Protect tags matching</span><input value={adding} placeholder="v*" onChange={(e) => setAdding(e.target.value)} /></label>
        <div><button type="button" className="btn btn-sm" disabled={!adding.trim() || rules.some((r) => r.pattern === adding.trim())}
          onClick={() => { setRules([...rules, blank(adding.trim())]); setAdding(""); setSaved(false); }}>Add the pattern</button><span className="faint small"> It takes effect when you save.</span></div>
        <ErrorBox error={error} />
        <div><button type="submit" className="btn btn-primary">Save the protected tags</button>{saved && <span className="faint"> Saved.</span>}</div>
      </form>
    </Sheet>
  );
}
