// Add an environment: its name and target, who may deploy there, whether
// it is protected and how its queue behaves, and optionally a deploy pool
// for its target.

import { useEffect, useState } from "react";
import { api, type RunnerPool } from "../../api";
import { type ChangeRequest, type ConfigOverview, type Spec } from "../../configApi";
import { Choices, DefinedAt, TextInput } from "../../components/ConfigForm";
import { Link } from "../../components/ui";
import { CreatedList, PickNames, WizardRunner, useNames, useObjects, type StepDef } from "./Wizards";

export function AddEnvironmentWizard({ overview }: { overview: ConfigOverview }) {
  const proj = overview.levels.project;
  const [name, setName] = useState("");
  const [target, setTarget] = useState("");
  const [unit, setUnit] = useState("");
  const [projects, setProjects] = useState<string[]>([]);
  const [prot, setProt] = useState<"no" | "yes">("no");
  const [queue, setQueue] = useState<"newest" | "all">("newest");
  const [poolChoice, setPoolChoice] = useState<"existing" | "new" | "later">("later");
  const [poolName, setPoolName] = useState("");
  const [pools, setPools] = useState<RunnerPool[]>([]);
  const projectNames = useNames("projects");
  const envs = useObjects("environments");
  const knownTargets = Array.from(new Set((envs ?? []).map((e) => String((e.spec as Spec).target ?? "")).filter(Boolean)));

  useEffect(() => { api.runnerPools().then((p) => setPools(p.pools)).catch(() => setPools([])); }, []);
  const serving = pools.filter((p) => p.kind === "deploy" && p.targets.includes(target.trim()));
  useEffect(() => {
    if (serving.length) setPoolChoice("existing");
    else if (poolChoice === "existing") setPoolChoice("later");
  }, [serving.length]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (!poolName && name) setPoolName(`deploy-${name.trim()}`); }, [name]); // eslint-disable-line react-hooks/exhaustive-deps

  const spec: Spec = { target: target.trim() };
  if (projects.length) spec.projects = projects;
  if (queue === "all") spec.queue = "all";
  if (prot === "yes") spec.protected = true;
  const changes: ChangeRequest[] = [];
  if (name.trim()) changes.push({ kind: "environment", name: name.trim(), unit: unit || undefined, spec, version: -1 });
  if (name.trim() && poolChoice === "new" && poolName.trim()) {
    changes.push({ kind: "runner-pool", name: poolName.trim(), unit: unit || undefined, spec: { kind: "deploy", targets: [target.trim()], require_identity: true }, version: -1 });
  }

  const steps: StepDef[] = [
    {
      id: "where", title: "Name and target",
      fields: [{ path: "name", match: ["name", "names", "exists", "already"], value: name }, { path: "target", value: target }],
      local: () => !name.trim() ? "Give the environment a name." : !target.trim() ? "Give its target." : null,
      render: (by) => (
        <>
          <p>An environment is a place deploy steps deploy to, such as staging or production. Pipelines name it in their deploy steps.</p>
          <div className="cf-grid">
            <TextInput label="Name" value={name} onChange={setName} mono required autoFocus placeholder="staging" problems={by.name}
              help="As pipelines will write it: environment: staging. Letters, digits, dots, hyphens and underscores." />
            <Field2 target={target} setTarget={setTarget} known={knownTargets} problems={by.target} />
            <DefinedAt overview={overview} value={unit} onChange={setUnit} />
          </div>
        </>
      ),
    },
    {
      id: "who", title: "Who may deploy there",
      fields: [{ path: "projects", value: projects }],
      local: () => !projects.length ? `Choose at least one ${proj.singular.toLowerCase()} (or * for all); otherwise nothing could deploy here.` : null,
      render: (by) => (
        <PickNames legend={`${proj.plural} that may deploy here`} names={["*", ...projectNames]} value={projects} onChange={setProjects} problems={by.projects}
          help={`A deploy step of a ${proj.singular.toLowerCase()} not listed fails at once, before anything changes. * allows every ${proj.singular.toLowerCase()}; a pattern such as web-* allows a family.`} />
      ),
    },
    {
      id: "rules", title: "Protection and queue",
      fields: [{ path: "protected" }, { path: "queue" }],
      render: (by) => (
        <>
          <Choices legend="Is it protected?" value={prot} onChange={(v) => setProt(v as "no" | "yes")} problems={by.protected}
            options={[
              { value: "no", label: "No", help: "Whoever may operate the project may promote or retry its deployments here, including the person who started the run. Usual for test and staging environments." },
              { value: "yes", label: "Yes, protected", help: "The person who started a run may not promote or retry its deployment here: someone else with the operator role must. Anyone with the operator role may still abort. The usual choice for production." },
            ]} />
          <Choices legend="When several runs wait to deploy the same service here" value={queue} onChange={(v) => setQueue(v as "newest" | "all")} problems={by.queue}
            options={[
              { value: "newest", label: "Only the newest deploys", badge: "the default", help: "A run waiting for the environment is skipped when a newer run of the same service starts waiting, so the latest version arrives soonest." },
              { value: "all", label: "Every run deploys, in turn", help: "Each waiting run deploys in order. Slower, but every version passes through the environment (useful when each must be tested there)." },
            ]} />
          <div className="callout callout-info">
            <strong>Approvals.</strong> To have someone approve before a deployment here, either add an approval step before the deploy step in the pipeline,
            or <Link href="/config/wizard/add-policy">add a policy</Link> with the <span className="mono">approvals</span> rule for this environment, which every pipeline must then meet.
          </div>
        </>
      ),
    },
    {
      id: "pool", title: "Deploy runners",
      fields: [{ path: "pool", match: ["runner_pools", "pool"], value: poolChoice === "new" ? poolName : "" }],
      render: (by) => (
        <>
          <p>Deployments here are done by deploy runners that serve target <span className="mono">{target || "…"}</span>. Until one does, deploy steps to this environment wait.</p>
          <Choices legend="Which runners deploy here?" value={poolChoice} onChange={(v) => setPoolChoice(v as "existing" | "new" | "later")}
            options={[
              ...(serving.length ? [{ value: "existing", label: `The pool that already serves ${target}: ${serving.map((p) => p.name).join(", ")}`, help: "Nothing to add: its runners will take this environment's deployments." }] : []),
              { value: "new", label: "A new deploy pool for this target", help: "Made with the environment. Then add a runner to it: the next wizard gives you the command." },
              { value: "later", label: "Decide later", help: "The environment works as soon as a deploy pool lists its target." },
            ]} />
          {poolChoice === "new" && (
            <div className="cf-grid">
              <TextInput label="Name of the pool" value={poolName} onChange={setPoolName} mono problems={by.pool}
                help="It takes only runners enrolled with a join token for it, so no shared token can deploy here." />
            </div>
          )}
        </>
      ),
    },
  ];

  return (
    <WizardRunner title="Add an environment" overview={overview}
      intro="A place to deploy to. Nothing is created until you confirm at the end."
      steps={steps} changes={changes}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <h3 className="h-sm">Next</h3>
          <ul className="cf-next">
            {poolChoice === "new" && <li><Link href={`/config/wizard/add-runner?pool=${encodeURIComponent(poolName.trim())}`}>Add a runner to pool {poolName.trim()}</Link></li>}
            <li><Link href="/environments">See the environments</Link></li>
            <li>Deploy there from a pipeline: a step with <span className="mono">kind: deploy</span> and <span className="mono">environment: {name.trim()}</span></li>
          </ul>
        </>
      )} />
  );
}

/** The target, with the targets other environments have as suggestions. */
function Field2({ target, setTarget, known, problems }: { target: string; setTarget: (v: string) => void; known: string[]; problems?: string[] }) {
  return (
    <>
      <TextInput label="Target" value={target} onChange={setTarget} mono required placeholder="prod-cluster" problems={problems}
        help={`What deploy runners serve to reach it: in practice, its cluster. Environments in one cluster share a target.${known.length ? ` In use: ${known.join(", ")}.` : ""}`} />
    </>
  );
}
