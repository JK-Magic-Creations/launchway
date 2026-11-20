# AI models

Optional models for steps, for explaining verdicts and for explaining failed steps. Nothing is sent to a model unless an operator configured one.

Nothing is sent to any model unless an operator configured one. With none, every AI feature says so
and everything else works. Models are named in the server's configuration (Helm: `ai.models`):

```yaml
ai:
  models:
    - name: local                     # what pipelines call it
      provider: openai                # OpenAI-compatible chat completions: Ollama, vLLM, LM Studio...
      endpoint: http://ollama.ai.svc:11434/v1
      model: qwen3:8b
      projects: ["*"]
      max_tokens: 2048                # per call
      calls_per_run: 10
      tokens_per_day: 2000000         # per project, input and output
    - name: claude
      provider: anthropic             # the Messages API
      model: claude-sonnet-5-5
      key: { store: vault, key: ci/anthropic, field: api_key }   # a reference, never a value
      projects: [shop]
      tokens_per_day: 500000
runner:
  ai: true                            # this runner may make the calls (default false)
```

The call is made by a runner (`ai: true` in its configuration) with the key from its own secret
stores; the key never enters a step's container. The servers themselves call a model only for
**Explain** (below), with keys from `ai.secrets`, stores of their own (Helm:
`ai.serverSecretStores`); without them they explain only with models that need no key. Adding another provider
is one small file in `internal/ai`. `GET /api/v1/ai/models` and `launchway models` list the names,
limits and today's use, never endpoints or keys.

## An ai step that writes release notes

```yaml
steps:
  changes:
    image: alpine/git:2.47.2
    run: |
      mkdir -p out
      prev=$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || git rev-list --max-parents=0 HEAD)
      git log --format='%h %s' "$prev"..HEAD > out/commits.txt
    artifacts:
      - { name: log, paths: [out/commits.txt] }

  notes:
    kind: ai
    needs: changes
    model: local
    instructions: You write short release notes for operators. Plain words, no marketing.
    prompt: Write the release notes for these commits, and say how risky the release looks.
    inputs:
      - { artifact: changes/log, file: out/commits.txt, label: commits, max_bytes: 32768 }
    schema:                           # optional: the answer must be this JSON
      type: object
      properties:
        notes: { type: string }
        risk:  { type: string, enum: [low, medium, high] }
      required: [notes, risk]
    max_tokens: 800

  publish:
    needs: notes
    image: alpine:3.22
    run: |
      echo "risk: $LAUNCHWAY_NEEDS_NOTES_RISK"
      printf '%s\n' "$LAUNCHWAY_NEEDS_NOTES_NOTES" > RELEASE_NOTES.md
```

Each scalar property of the schema becomes an output (`notes`, `risk`), next to `text`, `model`,
`input_tokens` and `output_tokens`; later steps use them like any output (`{ output: notes.risk }`).
An answer that does not follow the schema fails the step. The whole answer is kept as the artifact
`response`, and the step shows the tokens used and the estimated cost. The model's answer is
untrusted text: it can set outputs and an artifact, never run anything. A pull-request run gets no
model unless the project allows secrets on pull requests.

## Models on verifications and failed steps

A verify step with `model: local` sends the model what the statistics computed and the top log
clusters, and shows its explanation and suggested cause next to the verdict. The decision stays
with the statistics. With `decide: model-assisted` the model may turn a pass into inconclusive or
a fail, never a fail into a pass, and its reasoning is recorded with the verdict. On the run page,
**Explain** on a failed step asks a configured model to summarise the step's log tail, failed tests
and findings (`POST /api/v1/runs/{run}/steps/{step}/explain`); the button is not there when no
model is configured.

**What is sent.** For an ai step: its instructions, its prompt and its inputs, each cut to its
`max_bytes` and the model's `max_input_bytes`, with every secret the runner knows masked first. For
a verification: the service, the environment, each metric's means and standard deviations and the
reason it passed or failed, and up to 20 log templates per log query with one redacted sample line
each; never the queries, the sources' addresses or credentials. For Explain: the failing step's
last log lines, failed test names and messages and findings, redacted.

**Prompt injection.** Log lines and commit messages are written by applications and people, and
can contain text that reads like instructions to a model. The instructions say to treat them as
data, but that is not a guarantee. What limits the harm is what a model's answer can do: in a
verification it can only make the verdict stricter (`model-assisted` turns a pass into
inconclusive or a fail; nothing a log line says can make a failing release pass), and in an ai step
it can only set outputs and an artifact, which later steps should treat as untrusted input.

**Where the data go.** A hosted model receives the data above in the provider's region, under the
provider's terms; check that this is allowed for your logs and code before you configure one. A
local model keeps everything on your own machines.

## A local model, step by step

1. Run a model server next to the runners, for example Ollama:
   `podman run -d --name ollama -p 11434:11434 -v ollama:/root/.ollama docker.io/ollama/ollama`
   and pull a model: `podman exec ollama ollama pull qwen3:8b` (a smaller one, such as
   `qwen2.5:0.5b`, is enough to try it out).
2. Add it to the servers' configuration as `provider: openai`, `endpoint:
   http://<host>:11434/v1`, `model: qwen3:8b`, with no `key`, and list the projects that may use it.
3. Set `ai: true` on the runners that should make the calls (with Helm: `runner.ai: true`, and
   `delivery.deployRunner.ai: true` for verification explanations).
4. Use `model: local` in an ai step or a verify step. `make test-local-model` runs the same thing
   against a real small model in a container.

AI steps need schema 19, like verify steps.
