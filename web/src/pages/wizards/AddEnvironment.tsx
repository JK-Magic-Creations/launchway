// Add an environment: its name and target, who may deploy there, whether
// it is protected and how its queue behaves, and optionally a deploy pool
// for its target.

import { useEffect, useState } from "react";
import { api, type RunnerPool } from "../../api";
import { type ChangeRequest, type ConfigOverview, type Spec } from "../../configApi";
import { Choices, DefinedAt, TextInput } from "../../components/ConfigForm";
import { Link } from "../../components/ui";
import { CreatedList, PickNames, WizardRunner, useNames, useObjects, type StepDef } from "./Wizards";
import { terms } from "../../terms";
import { prose } from "../../prose";
const P = await prose("pages.wizards.AddEnvironment");

export function AddEnvironmentWizard({ overview }: { overview: ConfigOverview }) {
  const proj = overview.levels.project;
  const [name, setName] = useState("");
  const [target, setTarget] = useState("");
  const [unit, setUnit] = useState("");
  const [projects, setProjects] = useState<string[]>([]);
  const [protChoice, setProtChoice] = useState<"no" | "yes" | null>(null);
  // Production is usually protected: the default follows the name until someone chooses.
  const prot = protChoice ?? (/prod|live/i.test(name) ? "yes" : "no");
  const [queue, setQueue] = useState<"newest" | "all">("newest");
  const [poolPick, setPoolPick] = useState<"existing" | "new" | "later" | null>(null);
  const [poolNameEdit, setPoolNameEdit] = useState<string | null>(null);
  const [pools, setPools] = useState<RunnerPool[]>([]);
  const projectNames = useNames("projects");
  const envs = useObjects("environments");
  const knownTargets = Array.from(new Set((envs ?? []).map((e) => String((e.spec as Spec).target ?? "")).filter(Boolean)));

  useEffect(() => { api.runnerPools().then((p) => setPools(p.pools)).catch(() => setPools([])); }, []);
  const serving = pools.filter((p) => p.kind === "deploy" && p.targets.includes(target.trim()));
  // Until someone chooses, runners already serving the target are the default; "existing" needs some.
  const poolChoice: "existing" | "new" | "later" = poolPick === null ? (serving.length ? "existing" : "later") : poolPick === "existing" && !serving.length ? "later" : poolPick;
  const poolName = poolNameEdit ?? (name.trim() ? `deploy-${name.trim()}` : "");

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
      local: () => !name.trim() ? terms().say(P.t0) : !target.trim() ? "Give its target." : null,
      render: (by) => (
        <>
          <p>{terms().say(P.t1)}</p>
          <div className="cf-grid">
            <TextInput label="Name" value={name} onChange={setName} mono required focusOnMount placeholder="staging" problems={by.name}
              help={terms().say(P.t2)} />
            <Field2 target={target} setTarget={setTarget} known={knownTargets} problems={by.target} />
            <DefinedAt overview={overview} value={unit} onChange={setUnit} />
          </div>
        </>
      ),
    },
    {
      id: "who", title: terms().say("Who may {deploy:do} there"),
      fields: [{ path: "projects", value: projects }],
      local: () => !projects.length ? terms().say(`Choose at least one ${proj.singular.toLowerCase()} (or * for all); otherwise nothing could {deploy:do} here.`) : null,
      render: (by) => (
        <PickNames legend={terms().say(`${proj.plural} that may {deploy:do} here`)} names={["*", ...projectNames]} value={projects} onChange={setProjects} problems={by.projects}
          help={`A deploy step of a ${proj.singular.toLowerCase()} not listed fails at once, before anything changes. * allows every ${proj.singular.toLowerCase()}; a pattern such as web-* allows a family.`} />
      ),
    },
    {
      id: "rules", title: "Protection and queue",
      fields: [{ path: "protected" }, { path: "queue" }],
      render: (by) => (
        <>
          <Choices legend="Is it protected?" value={prot} onChange={(v) => setProtChoice(v as "no" | "yes")} problems={by.protected}
            options={[
              { value: "no", label: "No", help: terms().say(`Whoever may operate the ${proj.singular.toLowerCase()} may promote or retry its deployments here, including the person who started the {run}. Usual for test and staging {environments}.`) },
              { value: "yes", label: "Yes, protected", help: P.t3 },
            ]} />
          <Choices legend={terms().say(P.t4)} value={queue} onChange={(v) => setQueue(v as "newest" | "all")} problems={by.queue}
            options={[
              { value: "newest", label: terms().say("Only the newest {deploy:does}"), badge: "the default", help: terms().say(P.t5) },
              { value: "all", label: terms().say(P.t6), help: terms().say(P.t7) },
            ]} />
          <div className="callout callout-info">
            <strong>{terms().label("clearance")} and {terms().lower("preflight")}.</strong> To have someone give {terms().lower("clearance")} {P.t8}<span className="mono">approval</span>{P.t9}<Link href="/config/wizard/add-policy">add a policy</Link> with the <span className="mono">approvals</span> {terms().say(P.t10)} {terms().lower("preflight")}.
          </div>
        </>
      ),
    },
    {
      id: "pool", title: terms().say("{Deploy:do} {runners}"),
      fields: [{ path: "pool", match: ["runner_pools", "pool"], value: poolChoice === "new" ? poolName : "" }],
      render: (by) => (
        <>
          <p>{terms().say(P.t11)} <span className="mono">{target || "…"}</span>{terms().say(P.t12)}</p>
          <Choices legend={terms().say(P.t13)} value={poolChoice} onChange={(v) => setPoolPick(v as "existing" | "new" | "later")}
            options={[
              ...(serving.length ? [{ value: "existing", label: `The pool that already serves ${target}: ${serving.map((p) => p.name).join(", ")}`, help: terms().say(P.t14) }] : []),
              { value: "new", label: terms().say(P.t15), help: terms().say(P.t16) },
              { value: "later", label: "Decide later", help: terms().say(P.t17) },
            ]} />
          {poolChoice === "new" && (
            <div className="cf-grid">
              <TextInput label="Name of the pool" value={poolName} onChange={setPoolNameEdit} mono problems={by.pool}
                help={terms().say(P.t18)} />
            </div>
          )}
        </>
      ),
    },
  ];

  return (
    <WizardRunner title={terms().say("Add an {environment}")} overview={overview}
      intro={terms().say(P.t19)}
      steps={steps} changes={changes}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <h3 className="h-sm">Next</h3>
          <ul className="cf-next">
            {poolChoice === "new" && <li><Link href={`/config/wizard/add-runner?pool=${encodeURIComponent(poolName.trim())}`}>{terms().say("Add a {runner} to pool")} {poolName.trim()}</Link></li>}
            <li><Link href="/environments">{terms().say("See the {environments}")}</Link></li>
            <li>{terms().say(P.t20)} <span className="mono">kind: deploy</span> and <span className="mono">environment: {name.trim()}</span></li>
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
        help={terms().say(`What {deploy:do} {runners} serve to reach it: in practice, its cluster. {Environments} in one cluster share a target.${known.length ? ` In use: ${known.join(", ")}.` : ""}`)} />
    </>
  );
}
