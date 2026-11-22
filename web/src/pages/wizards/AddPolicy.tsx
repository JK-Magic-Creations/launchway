// Add a policy: rules from the server's catalogue, enforce or report only,
// what it applies to, and its name.

import { useEffect, useState } from "react";
import { api, type RuleInfo } from "../../api";
import { type ChangeRequest, type ConfigOverview, type Spec } from "../../configApi";
import { EVENTS } from "../../configKinds";
import { Choices, DefinedAt, ListInput, TextInput } from "../../components/ConfigForm";
import { PolicyRules } from "../../components/PolicyRules";
import { Link } from "../../components/ui";
import { CreatedList, PickNames, WizardRunner, useNames, type StepDef } from "./Wizards";
import { terms } from "../../terms";
import { prose } from "../../prose";
const P = await prose("pages.wizards.AddPolicy");

export function AddPolicyWizard({ overview }: { overview: ConfigOverview }) {
  const proj = overview.levels.project;
  const [catalogue, setCatalogue] = useState<RuleInfo[] | null>(null);
  const [catError, setCatError] = useState("");
  const [rules, setRules] = useState<Record<string, unknown>>({});
  const [mode, setMode] = useState<"report" | "enforce">("report");
  const [projects, setProjects] = useState<string[]>([]);
  const [environments, setEnvironments] = useState<string[]>([]);
  const [branches, setBranches] = useState<string[]>([]);
  const [events, setEvents] = useState<string[]>([]);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [unit, setUnit] = useState("");
  const projectNames = useNames("projects");
  const envNames = useNames("environments");
  useEffect(() => { api.policies().then((p) => setCatalogue(p.catalogue)).catch((e) => { setCatalogue([]); setCatError(e instanceof Error ? e.message : String(e)); }); }, []);

  const deployRules = (catalogue ?? []).filter((r) => r.point === "deploy").map((r) => r.name);
  const hasDeploy = Object.keys(rules).some((r) => deployRules.includes(r));
  const scope: Spec = {};
  if (projects.length) scope.projects = projects;
  if (environments.length && hasDeploy) scope.environments = environments;
  if (branches.length) scope.branches = branches;
  if (events.length) scope.events = events;
  const spec: Spec = { mode, rules };
  if (description.trim()) spec.description = description.trim();
  if (Object.keys(scope).length) spec.scope = scope;
  const changes: ChangeRequest[] = name.trim() && Object.keys(rules).length ? [{ kind: "policy", name: name.trim(), unit: unit || undefined, spec, version: -1 }]
    : Object.keys(rules).length ? [{ kind: "policy", name: "new-policy-draft", spec, version: -1 }] : [];
  // Before the policy is named, the rules are checked under a stand-in name; the summary needs the real one.
  const realChanges = name.trim() ? changes : [];

  const steps: StepDef[] = [
    {
      id: "rules", title: "What it checks",
      fields: (catalogue ?? []).map((r) => ({ path: r.name, value: rules[r.name] })),
      local: () => !Object.keys(rules).length ? "Tick at least one rule." : null,
      render: (by) => catalogue === null ? <p className="faint">Reading the rule catalog…</p> : (
        <>
          {catError && <p className="cf-problem">{P.t0}{catError}</p>}
          <p>{P.t1}</p>
          <PolicyRules catalogue={catalogue} rules={rules} onChange={setRules} problems={by} />
        </>
      ),
    },
    {
      id: "mode", title: "Enforce or report",
      fields: [{ path: "mode" }],
      render: (by) => (
        <Choices legend={P.t2} value={mode} onChange={(v) => setMode(v as "report" | "enforce")} problems={by.mode}
          options={[
            { value: "report", label: "Report only", badge: "start here", help: terms().say(P.t3) },
            { value: "enforce", label: "Enforce", help: terms().say(P.t4) },
          ]} />
      ),
    },
    {
      id: "scope", title: "What it applies to",
      fields: [{ path: "scope", match: ["scope", "projects", "environments", "branches", "events"] }],
      render: (by) => (
        <>
          <PickNames legend={proj.plural} names={["*", ...projectNames]} value={projects} onChange={setProjects} problems={by.scope}
            help={`Nothing ticked: every ${proj.singular.toLowerCase()}.`} />
          {hasDeploy ? (
            <PickNames legend={terms().say("{Environments}")} names={envNames} value={environments} onChange={setEnvironments}
              help={terms().say(P.t5)} />
          ) : <p className="cf-help">{terms().say(P.t6)}</p>}
          <div className="cf-grid">
            <ListInput label="Branches" value={branches} onChange={setBranches} placeholder="main, release/*"
              help={P.t7} />
          </div>
          <PickNames legend="Events" names={EVENTS} value={events} onChange={setEvents} help={terms().say(P.t8)} placeholder="" />
        </>
      ),
    },
    {
      id: "name", title: "Name",
      fields: [{ path: "name", match: ["name", "names", "exists", "already"], value: name }, { path: "description", value: description }],
      local: () => !name.trim() ? "Give the policy a name." : null,
      render: (by) => (
        <div className="cf-grid">
          <TextInput label="Name" value={name} onChange={(v) => setName(v.toLowerCase())} mono required placeholder="prod-evidence" problems={by.name}
            help={P.t9} />
          <TextInput label="Description" value={description} onChange={setDescription} wide problems={by.description}
            placeholder={P.t10} help={P.t11} />
          <DefinedAt overview={overview} value={unit} onChange={setUnit} />
        </div>
      ),
    },
  ];

  return (
    <WizardRunner title="Add a policy" overview={overview}
      intro={P.t12}
      steps={steps} changes={name.trim() ? realChanges : changes}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <h3 className="h-sm">Next</h3>
          <ul className="cf-next">
            <li><Link href="/policy">{P.t13}</Link></li>
            {mode === "report" && <li>{P.t14}<Link href={`/config/policies/${encodeURIComponent(name.trim())}`}>its page</Link>.</li>}
          </ul>
        </>
      )} />
  );
}
