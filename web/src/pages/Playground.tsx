import { useEffect, useMemo, useState } from "react";
import { api, type StepView, type ValidateResult } from "../api";
import { Graph } from "../components/Graph";
import { ErrorBox } from "../components/ui";

const SAMPLE = `pipeline: my-service
on:
  push: { branches: [main, "release/*"] }

steps:
  lint:
    image: golang:1.24
    run: go vet ./...

  test:
    image: golang:1.24
    run: go test ./...
    retry: 1

  build:
    image: alpine:3.22
    needs: [lint, test]
    run: |
      echo "building $SLIPWAY_COMMIT"
      echo digest=sha256:example >> "$SLIPWAY_OUTPUTS"

  deploy:
    image: alpine:3.22
    needs: build
    when: { branch: main }
    run: echo "deploying $SLIPWAY_NEEDS_BUILD_DIGEST"
`;

const KEY = "slipway.playground";

// The playground checks a pipeline as you type and draws the graph it
// produces. Nothing runs.
export function PlaygroundPage() {
  const [text, setText] = useState(() => {
    try { return localStorage.getItem(KEY) || SAMPLE; } catch { return SAMPLE; }
  });
  const [res, setRes] = useState<ValidateResult | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [sel, setSel] = useState<string | null>(null);

  useEffect(() => {
    try { localStorage.setItem(KEY, text); } catch { /* private mode */ }
    const t = setTimeout(() => {
      api.validate(text).then((r) => { setRes(r); setError(null); }).catch(setError);
    }, 250);
    return () => clearTimeout(t);
  }, [text]);

  const steps: StepView[] = useMemo(() => (res?.graph?.steps ?? []).map((s, i) => ({
    run_id: "", id: s.id, position: i, status: "pending", attempt: 0, queued_at: 0, started_at: 0, finished_at: 0,
    exit_code: null, error: "", skip_kind: "", runner: "", outputs: {}, image: s.image, run: s.run, needs: s.needs,
    when: s.when, retry: s.retry, timeout: s.timeout,
  })), [res]);
  const chosen = res?.graph?.steps.find((s) => s.id === sel);
  const lines = text.split("\n").length;

  return (
    <div className="page page-wide">
      <header className="page-head">
        <div>
          <h1>Playground</h1>
          <p className="sub">Write a pipeline and see the graph it becomes. Checked by the same code the server uses; nothing runs.</p>
        </div>
        <button type="button" className="btn" onClick={() => setText(SAMPLE)}>Reset example</button>
      </header>
      <div className="split split-play">
        <section className="card card-flush editor">
          <div className="editor-gutter" aria-hidden="true">
            {Array.from({ length: lines }, (_, i) => {
              const bad = res?.problems?.some((p) => p.line === i + 1);
              return <div key={i} className={bad ? "gutter-bad" : ""}>{i + 1}</div>;
            })}
          </div>
          <label className="visually-hidden" htmlFor="pipeline-src">Pipeline file</label>
          <textarea id="pipeline-src" spellCheck={false} value={text} onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Tab") {
                e.preventDefault();
                const el = e.currentTarget, s = el.selectionStart, end = el.selectionEnd;
                const v = text.slice(0, s) + "  " + text.slice(end);
                setText(v);
                requestAnimationFrame(() => { el.selectionStart = el.selectionEnd = s + 2; });
              }
            }} />
        </section>
        <div className="play-right">
          <ErrorBox error={error} />
          {res && !res.ok && (
            <section className="card">
              <h2 className="h-sm bad">{res.problems?.length} problem{res.problems?.length === 1 ? "" : "s"}</h2>
              <ul className="problems">
                {res.problems?.map((p, i) => (
                  <li key={i}>{p.line ? <span className="mono faint">line {p.line}</span> : null} {p.path && <span className="mono">{p.path}</span>} {p.message}</li>
                ))}
              </ul>
            </section>
          )}
          {res?.ok && res.graph && res.levels && (
            <>
              <section className="card">
                <h2 className="h-sm ok">Valid: {res.graph.name}, {res.graph.steps.length} steps in {res.levels.length} stages</h2>
                <p className="sub">
                  {res.graph.on.push
                    ? <>Starts on pushes to {[...(res.graph.on.push.branches ?? []), ...(res.graph.on.push.tags ?? []).map((t) => "tag " + t)].join(", ")}, and by hand.</>
                    : <>Starts only by hand: there is no push trigger.</>}
                </p>
              </section>
              <section className="card card-flush">
                <Graph levels={res.levels} steps={steps} selected={sel} onSelect={setSel} now={0} compact
                  describe={(s) => s.when ? "conditional" : (s.image ?? "").replace(/^docker\.io\/library\//, "")} />
              </section>
              {chosen && (
                <section className="card">
                  <h2 className="h-sm">{chosen.id}</h2>
                  <dl className="kv">
                    <dt>Image</dt><dd className="mono">{chosen.image}</dd>
                    {chosen.needs && <><dt>Needs</dt><dd>{chosen.needs.join(", ")}</dd></>}
                    {chosen.when && <><dt>When</dt><dd className="mono">{JSON.stringify(chosen.when)}</dd></>}
                    <dt>Retry</dt><dd>{chosen.retry}</dd>
                    <dt>Timeout</dt><dd>{chosen.timeout}</dd>
                  </dl>
                  <pre className="code">{chosen.run}</pre>
                </section>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
