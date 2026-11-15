import { useEffect, useRef, useState } from "react";
import { PipelineEditor, type PipelineEditorHandle } from "../components/PipelineEditor";
import { PipelinePreview } from "../components/PipelinePreview";
import { Link } from "../components/ui";
import { useValidation } from "../validation";

const SAMPLE = `pipeline: my-service
on:
  push: { branches: [main, "release/*"] }

# Toolboxes, named once and shared by steps. Services run
# beside the steps and are reachable at localhost.
containers:
  go:    { image: golang:1.27, memory: 2Gi }
  db:    { image: postgres:17, service: true, env: { POSTGRES_PASSWORD: test } }

steps:
  lint:
    description: Look for common mistakes
    container: go
    run: go vet ./...

  test:
    container: go
    run: go test ./...
    retry: 1

  build:
    image: alpine:3.22
    needs: [lint, test]
    run: |
      echo "building $LAUNCHWAY_COMMIT"
      echo digest=sha256:example >> "$LAUNCHWAY_OUTPUTS"

  deploy:
    image: alpine:3.22
    needs: build
    when: { branch: main }
    run: echo "deploying $LAUNCHWAY_NEEDS_BUILD_DIGEST"
`;

const KEY = "launchway.playground";
const EXPAND_KEY = "launchway.playground.expand";

// The playground checks a pipeline as you type and draws the graph it
// produces. Nothing runs, and nothing is saved anywhere but this browser.
export function PlaygroundPage() {
  const [text, setText] = useState(() => {
    try { return localStorage.getItem(KEY) || SAMPLE; } catch { return SAMPLE; }
  });
  // Expanding reads the server's libraries; it is a choice, since a pipeline
  // written here may name libraries the server does not have.
  const [expand, setExpand] = useState(() => {
    try { return localStorage.getItem(EXPAND_KEY) === "1"; } catch { return false; }
  });
  const { res, error } = useValidation(text, { expand });
  useEffect(() => {
    try { localStorage.setItem(EXPAND_KEY, expand ? "1" : "0"); } catch { /* private mode */ }
  }, [expand]);
  const editor = useRef<PipelineEditorHandle>(null);
  useEffect(() => {
    try { localStorage.setItem(KEY, text); } catch { /* private mode */ }
  }, [text]);

  return (
    <div className="page page-wide">
      <header className="page-head">
        <div>
          <h1>Playground</h1>
          <p className="sub">Write a pipeline and see the graph it becomes. Checked by the same code the server uses; nothing runs.
            To change a project's pipeline, open it from <Link href="/projects">Projects</Link>.</p>
        </div>
        <div className="run-actions">
          <label className="checkline" title="Steps that say uses: library/template are expanded from the libraries this server allows">
            <input type="checkbox" checked={expand} onChange={(e) => setExpand(e.target.checked)} /> Expand templates
          </label>
          <button type="button" className="btn" onClick={() => setText(SAMPLE)}>Reset example</button>
        </div>
      </header>
      <div className="split split-play">
        <section className="card card-flush">
          <PipelineEditor ref={editor} text={text} onChange={setText} problems={res?.ok ? [] : res?.problems} label="Pipeline file" />
        </section>
        <PipelinePreview res={res} error={error} onJump={(line) => editor.current?.goToLine(line)} />
      </div>
    </div>
  );
}
