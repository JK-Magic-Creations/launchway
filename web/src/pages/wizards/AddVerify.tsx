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
import { prose } from "../../prose";
const P = await prose("pages.wizards.AddVerify");

const URL_HINT: Record<string, string> = {
  prometheus: P.t0, loki: P.t1, clickhouse: P.t2,
  elasticsearch: P.t3, opensearch: P.t4, postgres: P.t5, http: "https://status.example.com",
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
          <p>{P.t6}</p>
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
              help={P.t7} />
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
            <strong>{terms().say(P.t8)}</strong>{P.t9}</div>
          <div className="cf-grid">
            <TextInput label={terms().say(P.t10)} value={url} onChange={setUrl} mono wide placeholder={URL_HINT[type]} />
            <SecretRefInput label={P.t11} value={secret} onChange={setSecret}
              help={terms().say(P.t12)} />
          </div>
          <CopyBlock title={terms().say(P.t13)} text={runnerYAML} note={terms().say(P.t14)} />
        </>
      ),
    },
  ];

  return (
    <WizardRunner title="Add a health source" overview={overview}
      intro={P.t15}
      steps={steps} changes={changes}
      summaryExtra={<CopyBlock title={terms().say(P.t16)} text={runnerYAML} />}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <CopyBlock title={terms().say(P.t17)} text={runnerYAML} note={terms().say(P.t18)} />
          <ul className="cf-next"><li><Link href="/runners">{terms().say(P.t19)}</Link></li></ul>
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
          <p>{P.t20}</p>
          <Choices legend="Where is the model?" value={provider} onChange={(v) => { setProvider(v as typeof provider); setKey(null); }} problems={by.provider}
            options={[
              { value: "anthropic", label: P.t21, help: P.t22 },
              { value: "openai-hosted", label: P.t23, help: P.t24 },
              { value: "openai-local", label: P.t25, help: P.t26 },
            ]} />
        </>
      ),
    },
    {
      id: "model", title: "The model and its key",
      fields: [{ path: "name", match: ["name", "names", "exists", "already"], value: name }, { path: "model", value: model }, { path: "endpoint", value: endpoint }, { path: "key", match: ["key is required", "key"], value: key }],
      local: () => !name.trim() ? P.t27 : !model.trim() ? "Give the provider's model id." : prov === "openai" && !endpoint.trim() ? P.t28 : null,
      render: (by) => (
        <>
          <div className="cf-grid">
            <TextInput label="Name pipelines use" value={name} onChange={(v) => setName(v.toLowerCase())} mono required placeholder="reviewer" problems={by.name}
              help={P.t29} />
            <TextInput label="The provider's model id" value={model} onChange={setModel} mono required problems={by.model}
              placeholder={prov === "anthropic" ? "claude-sonnet-5-5" : "qwen3:8b"} help={P.t30} />
            {prov === "openai" && <TextInput label="API address" value={endpoint} onChange={setEndpoint} mono wide required problems={by.endpoint}
              placeholder={provider === "openai-local" ? P.t31 : "https://api.example.com/v1"} help={P.t32} />}
            <DefinedAt overview={overview} value={unit} onChange={setUnit} />
          </div>
          <SecretRefInput label={provider === "openai-local" ? P.t33 : "API key (a reference)"} value={key} onChange={setKey} problems={by.key}
            help={terms().say(P.t34)} />
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
            help={P.t35} />
          <div className="cf-grid">
            <TextInput label="Output tokens per call" type="number" value={maxTokens} onChange={setMaxTokens} placeholder="4096" problems={by.max_tokens} help={P.t36} />
            <TextInput label={terms().say("Calls per {run}")} type="number" value={callsPerRun} onChange={setCallsPerRun} placeholder="20" problems={by.calls_per_run} help={P.t37} />
            <TextInput label={terms().say("Tokens per {project} per day")} type="number" value={tokensPerDay} onChange={setTokensPerDay} placeholder="no limit" problems={by.tokens_per_day} help={P.t38} />
          </div>
        </>
      ),
    },
  ];

  const runnerNote = key?.store ? terms().say(`# runner.yaml of the {runners} that run AI steps\nai: true\n# and a secret store named "${key.store}" in its secrets: section that holds ${key.key}${key.field ? "#" + key.field : ""}\n`) : terms().say(P.t39);
  return (
    <WizardRunner title="Add an AI model" overview={overview}
      intro={P.t40}
      steps={steps} changes={changes}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <CopyBlock title={terms().say("On the {runners}")} text={runnerNote} note={terms().say(P.t41)} />
          <ul className="cf-next"><li><Link href="/runners">{terms().say(P.t42)}</Link></li></ul>
        </>
      )} />
  );
}
