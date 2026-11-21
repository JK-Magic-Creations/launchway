import { useEffect, useMemo, useState } from "react";
import { ApiError } from "../api";
import { useMe } from "../auth";
import { configApi, loadOverview, type ConfigObject } from "../configApi";
import { ErrorBox } from "../components/ui";
import { WordsEditor, wordsProblems, type WordsValue } from "../components/WordsEditor";
import { Button, PageHead, Sheet } from "../kit/layout";
import { Icon } from "../kit/Icon";
import { isSetId, terms, type TermsConfig } from "../terms";

/** The words held in an installation's settings, with the names an older installation gave its levels carried over. */
export function wordsOf(spec: Record<string, unknown> | undefined): WordsValue {
  const t = (spec?.terms ?? {}) as { set?: string; overrides?: Record<string, string> };
  const overrides: Record<string, string> = {};
  const levels = (spec?.levels ?? {}) as TermsConfig["levels"];
  for (const [lk, key] of [["installation", "installation"], ["organisation", "organization"], ["unit", "unit"], ["project", "project"]] as const) {
    const l = levels?.[lk];
    if (l?.singular) overrides[key] = l.singular;
    if (l?.plural) overrides[key + "_plural"] = l.plural;
  }
  return { set: isSetId(t.set) ? t.set : "aviation", overrides: { ...overrides, ...(t.overrides ?? {}) } };
}

/** What to store for the words: nothing at all for the defaults. The older `levels` entry is folded into the renames. */
export function withWords(spec: Record<string, unknown>, v: WordsValue): Record<string, unknown> {
  const out = { ...spec };
  delete out.levels;
  if (v.set === "aviation" && Object.keys(v.overrides).length === 0) delete out.terms;
  else out.terms = { set: v.set, overrides: v.overrides };
  return out;
}

/** Settings → Words: which set of words Launchway uses, and any single word renamed. Labels only. */
export function SettingsWordsPage() {
  const me = useMe();
  const admin = !!me?.admin;
  const [obj, setObj] = useState<ConfigObject | null | undefined>(undefined);
  const [value, setValue] = useState<WordsValue>({ set: terms().set, overrides: { ...terms().overrides } });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (!admin) return;
    let alive = true;
    configApi.get("installation", "installation")
      .then((o) => { if (alive) { setObj(o); setValue(wordsOf(o.spec as Record<string, unknown>)); } })
      .catch((e) => { if (e instanceof ApiError && e.status === 404) { if (alive) setObj(null); } else if (alive) setError(e); });
    return () => { alive = false; };
  }, [admin]);

  const problem = useMemo(() => wordsProblems(value), [value]);
  const save = async () => {
    setBusy(true); setError(null); setSaved(false);
    try {
      const spec = withWords((obj?.spec ?? {}) as Record<string, unknown>, value);
      const r = await configApi.put("installation", "installation", { spec: spec as never, version: obj?.version ?? -1, comment: "Words: which set of names Launchway shows" });
      setObj(r.object); setSaved(true);
      await loadOverview(true);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };

  return (
    <div className="page">
      <PageHead eyebrow="Settings" title="Words"
        sentence="Launchway's own words for its levels and parts. Choose a set, or rename any word. They are labels only: addresses, commands and pipeline files keep their plain names." />
      {admin ? (
        <Sheet title="Choose the words" pad>
          <WordsEditor value={value} onChange={(v) => { setValue(v); setSaved(false); }} />
          <ErrorBox error={error} />
          <div className="acts">
            <Button kind="pri" icon="check" disabled={busy || obj === undefined || !!problem} onClick={save}>Save the words</Button>
            {problem && <span className="cf-problem" role="alert">{problem}</span>}
          </div>
          {saved && <div className="note" role="status"><Icon name="check" />Saved. The screens use the new words now, and every change is in the history of the {terms().lower("installation")} settings.</div>}
        </Sheet>
      ) : (
        <Sheet pad><div className="note"><Icon name="lock" />Only an administrator can change the words. Currently: the {terms().set === "business" ? "plain business" : "aviation"} set.</div></Sheet>
      )}
    </div>
  );
}
