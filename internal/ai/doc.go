// Package ai calls AI models for pipeline steps. It is an optional layer:
// Launchway works fully without it, and nothing is sent anywhere unless an
// operator has configured a model.
//
// The operator lists models in the server configuration (ai.models); each
// entry names a provider, an endpoint, the provider's model id, where the API
// key is kept, which projects may use it, and its limits. Two providers are
// built in:
//
//   - anthropic: the Anthropic Messages API (POST /v1/messages, API version
//     2026-10-21).
//   - openai: OpenAI-compatible chat completions (POST /chat/completions).
//     Ollama, vLLM, LM Studio and most other local model servers speak it, so
//     this is how a local model is configured, as a first-class choice.
//
// A local model needs no key:
//
//	ai:
//	  models:
//	    - name: local
//	      provider: openai
//	      endpoint: http://ollama.ai.svc:11434/v1
//	      model: qwen3:8b
//	      projects: ["*"]
//
// Adding another provider means implementing the small Provider interface.
//
// # Limits
//
// Every call is bounded: the prompt by max_input_bytes (an error, never a
// silent cut; callers shorten inputs themselves and say so), the output by
// max_tokens, the HTTP answer by 4 MiB and its text by 1 MiB, and the time by
// the model's timeout, retries included. Calls per run and tokens per day
// are recorded on the model for the caller to enforce, since only the caller
// knows runs and projects.
//
// # Model output is untrusted
//
// A model's answer is text written by a program that read whatever the
// pipeline put in its prompt: commit messages, diffs, logs, issue text. Any
// of those can carry instructions aimed at the model (prompt injection), so
// the answer must be treated like any other untrusted input. This package
// never executes, evaluates or follows anything in an answer; it only
// returns it, and checks it against a JSON schema when one is given. Callers
// may only set step outputs and artifacts from it. Anything that acts on an
// answer (a gate, a comment, a deployment) must treat it as data, quote it
// where it is shown, and never pass it to a shell or template.
//
// Secrets never go to a model: the API key is sent only in its header, and
// never appears in an error, log or response. Callers must not put secret
// values in prompts.
package ai
