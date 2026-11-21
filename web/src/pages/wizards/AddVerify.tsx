// Add a health source and add an AI model. Both are names pipelines use;
// the server keeps the name, the type and which projects may use it, while
// addresses and credentials stay with the runners (a model's key is a
// reference to a secret store the runners have, never a value).

import { useState } from "react";
import { type ChangeRequest, type ConfigOverview, type Spec } from "../../configApi";
import { HEALTH_TYPES } from "../../configKinds";
import { Choices, CopyBlock, DefinedAt, SecretRefInput, TextInput, type SecretRef } from "../../components/ConfigForm";
import { Link } from "../../components/ui";
import { CreatedList, PickNames, WizardRunner, useNames, type StepDef } from "./Wizards";
import { terms } from "../../terms";

const URL_HINT: Record<string, string> = {
  prometheus: "http://prometheus-operated.monitoring.svc:9090", loki: "http://loki-gateway.monitoring.svc", clickhouse: "http://clickhouse.logging.svc:8123",
  elasticsearch: "https://elasticsearch.logging.svc:9200", opensearch: "https://opensearch.logging.svc:9200", postgres: "postgres://reader@db.internal:5432/app", http: "https://status.example.com",
};

export function AddHealthSourceWizard({ overview }: { overview: ConfigOverview }) {
  const proj = overview.levels.project;
  const [type, setType] = useState("prometheus");
  const [name, setName] = useState("prometheus");
  const [nameTouched, setNameTouched] = useState(false);
  const [projects, setProjects] = useState<string[]>(["*"]);
  const [unit, setUnit] = useState("");
  const [url, setUrl] = useState("");
  const [secret, setSecret] = useState<SecretRef | null>(null);
  const projectNames = useNames("projects");

  const spec: Spec = { type, ...(projects.length ? { projects } : {}) };
  const changes: ChangeRequest[] = name.trim() ? [{ kind: "health-source", name: name.trim(), unit: unit || undefined, spec, version: -1 }] : [];
  const runnerYAML = terms().say(`# runner.yaml of the {deploy:do} {runners} that serve the {environments}\nhealth_sources:\n  ${name.trim() || "name"}:\n    type: ${type}\n    url: ${url || URL_HINT[type]}\n`) +
    (secret ? `    auth: { type: bearer, secret: { store: ${secret.store}, key: ${secret.key}${secret.field ? `, field: ${secret.field}` : ""} } }\n` : "") +
    `    projects: [${projects.map((p) => JSON.stringify(p)).join(", ")}]\n`;

  const steps: StepDef[] = [
    {
      id: "type", title: "What kind of system",
      fields: [{ path: "type" }],
      render: (by) => (
        <>
          <p>Verify steps judge a deployment by what your monitoring says: error rates, latency, log messages. A health source is a monitoring system they may read, by name.</p>
          <Choices legend="What kind of system is it?" value={type} onChange={(v) => { setType(v); if (!nameTouched) setName(v); }} problems={by.type} options={HEALTH_TYPES} />
        </>
      ),
    },
    {
      id: "who", title: "Name and who may read it",
      fields: [{ path: "name", match: ["name", "names", "exists", "already"], value: name }, { path: "projects", value: projects }],
      local: () => !name.trim() ? "Give it a name." : !projects.length ? `Choose the ${proj.plural.toLowerCase()} that may read it; with none, no pipeline could use it.` : null,
      render: (by) => (
        <>
          <div className="cf-grid">
            <TextInput label="Name" value={name} onChange={(v) => { setName(v); setNameTouched(true); }} mono required problems={by.name}
              help="What pipelines write in a verify step: source: prometheus." />
            <DefinedAt overview={overview} value={unit} onChange={setUnit} />
          </div>
          <PickNames legend={`${proj.plural} that may read it`} names={["*", ...projectNames]} value={projects} onChange={setProjects} problems={by.projects}
            help={`A ${proj.singular.toLowerCase()} not listed cannot name it in a verify step. Its queries run with the source's credentials, so list only those that should see this data.`} />
        </>
      ),
    },
    {
      id: "where", title: "Where it is",
      render: () => (
        <>
          <div className="callout callout-info">
            <strong>{terms().say("The address and credentials stay with the {deploy:do} {runners}")}</strong>, not on the server: they are next to your monitoring and run no one's scripts.
            Launchway's server keeps only the name, the type and who may use it. Nothing typed here is stored; it only fills in the lines below for you to copy.
          </div>
          <div className="cf-grid">
            <TextInput label={terms().say("Its address, as the {deploy:do} {runners} reach it")} value={url} onChange={setUrl} mono wide placeholder={URL_HINT[type]} />
            <SecretRefInput label="Its credentials, if it needs any (a reference)" value={secret} onChange={setSecret}
              help={terms().say("The secret store, key and field on the {runners} where the token or password is kept. Never the secret itself.")} />
          </div>
          <CopyBlock title={terms().say("For the {deploy:do} {runners}' runner.yaml")} text={runnerYAML} note={terms().say("Add these lines to every {deploy:do} {runner} serving the {environments} where verify steps will read it, then restart those {runners}.")} />
        </>
      ),
    },
  ];

  return (
    <WizardRunner title="Add a health source" overview={overview}
      intro="Metrics or logs that verify steps read to judge a deployment. Nothing is created until you confirm at the end."
      steps={steps} changes={changes}
      summaryExtra={<CopyBlock title={terms().say("Still to do on the {deploy:do} {runners}: runner.yaml")} text={runnerYAML} />}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <CopyBlock title={terms().say("On the {deploy:do} {runners}: runner.yaml")} text={runnerYAML} note={terms().say("Pipelines can name the source now; queries work once the {deploy:do} {runners} have these lines.")} />
          <ul className="cf-next"><li><Link href="/runners">{terms().say("See the health sources each {runner} serves")}</Link></li></ul>
        </>
      )} />
  );
}

export function AddModelWizard({ overview }: { overview: ConfigOverview }) {
  const proj = overview.levels.project;
  const [provider, setProvider] = useState<"anthropic" | "openai-hosted" | "openai-local">("anthropic");
  const [name, setName] = useState("");
  const [model, setModel] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [key, setKey] = useState<SecretRef | null>(null);
  const [projects, setProjects] = useState<string[]>([]);
  const [maxTokens, setMaxTokens] = useState("");
  const [callsPerRun, setCallsPerRun] = useState("");
  const [tokensPerDay, setTokensPerDay] = useState("");
  const [unit, setUnit] = useState("");
  const projectNames = useNames("projects");

  const prov = provider === "anthropic" ? "anthropic" : "openai";
  const spec: Spec = { provider: prov, model: model.trim() };
  if (endpoint.trim()) spec.endpoint = endpoint.trim();
  if (key && (key.store || key.key)) spec.key = key;
  if (projects.length) spec.projects = projects;
  if (maxTokens) spec.max_tokens = Number(maxTokens);
  if (callsPerRun) spec.calls_per_run = Number(callsPerRun);
  if (tokensPerDay) spec.tokens_per_day = Number(tokensPerDay);
  const changes: ChangeRequest[] = name.trim() ? [{ kind: "model", name: name.trim(), unit: unit || undefined, spec, version: -1 }] : [];

  const steps: StepDef[] = [
    {
      id: "provider", title: "Which kind of model",
      fields: [{ path: "provider" }],
      render: (by) => (
        <>
          <p>AI steps ask a model a question (summarize a failure, review a change); Launchway also uses one to explain failed steps. A model here is a name pipelines use, where it is, and who may use it.</p>
          <Choices legend="Where is the model?" value={provider} onChange={(v) => { setProvider(v as typeof provider); setKey(null); }} problems={by.provider}
            options={[
              { value: "anthropic", label: "Claude, through the Anthropic API", help: "Hosted models. Needs an API key, kept in a secret store. Prices are known, so each call shows its cost." },
              { value: "openai-hosted", label: "A hosted service with an OpenAI-compatible API", help: "Any service that speaks chat completions. Needs its address and usually a key." },
              { value: "openai-local", label: "A model server you run (OpenAI-compatible)", help: "Such as a local model server on your network: nothing leaves it, and it may need no key. Needs its address." },
            ]} />
        </>
      ),
    },
    {
      id: "model", title: "The model and its key",
      fields: [{ path: "name", match: ["name", "names", "exists", "already"], value: name }, { path: "model", value: model }, { path: "endpoint", value: endpoint }, { path: "key", match: ["key is required", "key"], value: key }],
      local: () => !name.trim() ? "Give the model a name for pipelines." : !model.trim() ? "Give the provider's model id." : prov === "openai" && !endpoint.trim() ? "Give the address of the model's API." : null,
      render: (by) => (
        <>
          <div className="cf-grid">
            <TextInput label="Name pipelines use" value={name} onChange={(v) => setName(v.toLowerCase())} mono required placeholder="reviewer" problems={by.name}
              help="Lower-case letters, digits and hyphens, at most 40 characters: model: reviewer in an AI step." />
            <TextInput label="The provider's model id" value={model} onChange={setModel} mono required problems={by.model}
              placeholder={prov === "anthropic" ? "claude-sonnet-5-5" : "qwen3:8b"} help="Exactly as the provider names it." />
            {prov === "openai" && <TextInput label="API address" value={endpoint} onChange={setEndpoint} mono wide required problems={by.endpoint}
              placeholder={provider === "openai-local" ? "http://models.internal:11434/v1" : "https://api.example.com/v1"} help="The base URL of its chat completions API." />}
            <DefinedAt overview={overview} value={unit} onChange={setUnit} />
          </div>
          <SecretRefInput label={provider === "openai-local" ? "API key (a reference; leave empty if it needs none)" : "API key (a reference)"} value={key} onChange={setKey} problems={by.key}
            help={terms().say("Where the key is kept: a secret store the {runners} (and the server, to explain failures) have, and the key's name and field there. The key itself is never typed into Launchway.")} />
        </>
      ),
    },
    {
      id: "use", title: `Who may use it, and limits`,
      fields: [{ path: "projects", value: projects }, { path: "max_tokens", value: maxTokens }, { path: "calls_per_run", value: callsPerRun }, { path: "tokens_per_day", value: tokensPerDay }],
      local: () => !projects.length ? `Choose the ${proj.plural.toLowerCase()} that may use it; with none, no pipeline could.` : null,
      render: (by) => (
        <>
          <PickNames legend={`${proj.plural} that may use it`} names={["*", ...projectNames]} value={projects} onChange={setProjects} problems={by.projects}
            help="Each call costs money or capacity and sends the step's text to the model: list only those that should." />
          <div className="cf-grid">
            <TextInput label="Output tokens per call" type="number" value={maxTokens} onChange={setMaxTokens} placeholder="4096" problems={by.max_tokens} help="The longest answer. Default 4096." />
            <TextInput label={terms().say("Calls per {run}")} type="number" value={callsPerRun} onChange={setCallsPerRun} placeholder="20" problems={by.calls_per_run} help="Stops a runaway loop. Default 20." />
            <TextInput label="Tokens per project per day" type="number" value={tokensPerDay} onChange={setTokensPerDay} placeholder="no limit" problems={by.tokens_per_day} help="A daily budget, input plus output. Empty: no limit." />
          </div>
        </>
      ),
    },
  ];

  const runnerNote = key?.store ? terms().say(`# runner.yaml of the {runners} that run AI steps\nai: true\n# and a secret store named "${key.store}" in its secrets: section that holds ${key.key}${key.field ? "#" + key.field : ""}\n`) : terms().say("# runner.yaml of the {runners} that run AI steps\nai: true\n");
  return (
    <WizardRunner title="Add an AI model" overview={overview}
      intro="A model AI steps may ask. Nothing is created until you confirm at the end."
      steps={steps} changes={changes}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <CopyBlock title={terms().say("On the {runners}")} text={runnerNote} note={terms().say("{Runners} call models only when their runner.yaml says ai: true, and read the key from their own secret stores.")} />
          <ul className="cf-next"><li><Link href="/runners">{terms().say("See which {runners} call models")}</Link></li></ul>
        </>
      )} />
  );
}
