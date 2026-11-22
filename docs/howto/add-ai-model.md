# Add an AI model

AI is optional in Launchway: with no model, nothing is ever sent to one. A model entry says which
model steps may ask, who may use it and what it may cost.

## What it asks

1. **Which kind?** A hosted model such as Claude, or any server with an OpenAI-compatible interface,
   including a model that runs on your own hardware (Ollama, vLLM). For a local model the wizard asks
   for its address; a hosted one has a default.
2. **Which model, and what do you call it?** The provider's model name, and the short name pipelines
   use.
3. **Where is the key?** Keys are never typed here. Choose the secret store and the key in it
   (a reference such as `vault`, `ai/anthropic`, `key`); a local model may need none.
4. **Which Flights (codebase) may use it?** Names or patterns; none by default.
5. **Limits.** The most output per call, calls per Sortie (run) and tokens per Flight per day, so a loop in a
   pipeline cannot spend without bound.

## What it creates

The model. Today's use is shown in **AI models** and with `launchway models`.

From the command line: `launchway models set claude -f model.yaml`.
