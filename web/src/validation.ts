import { useEffect, useState } from "react";
import { api, type ValidateResult } from "./api";

// useValidation checks pipeline text with the server's own parser as it
// changes, a moment after typing stops. Nothing is stored. With a project
// (or expand), steps that use templates are expanded from the server's
// libraries, as a run would expand them.
export function useValidation(text: string | null, opts: { project?: string; expand?: boolean } = {}, delay = 250):
  { res: ValidateResult | null; error: unknown; checking: boolean } {
  const { project, expand } = opts;
  const [res, setRes] = useState<ValidateResult | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [checking, setChecking] = useState(false);
  useEffect(() => {
    if (text === null) return;
    let alive = true;
    setChecking(true);
    const t = setTimeout(() => {
      api.validate(text, { project, expand })
        .then((r) => { if (alive) { setRes(r); setError(null); } })
        .catch((e) => { if (alive) setError(e); })
        .finally(() => { if (alive) setChecking(false); });
    }, delay);
    return () => { alive = false; clearTimeout(t); };
  }, [text, delay, project, expand]);
  return { res, error, checking };
}
