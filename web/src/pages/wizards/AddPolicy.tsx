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
          {catError && <p className="cf-problem">The catalog could not be read: {catError}</p>}
          <p>A policy is a set of rules every run it applies to must meet. There is no policy language to learn: each rule checks one thing, and the pipeline's authors cannot switch it off.</p>
          <PolicyRules catalogue={catalogue} rules={rules} onChange={setRules} problems={by} />
        </>
      ),
    },
    {
      id: "mode", title: "Enforce or report",
      fields: [{ path: "mode" }],
      render: (by) => (
        <Choices legend="What happens when a rule fails?" value={mode} onChange={(v) => setMode(v as "report" | "enforce")} problems={by.mode}
          options={[
            { value: "report", label: "Report only", badge: "start here", help: terms().say("The verdict is recorded and shown as a warning on the {run}; nothing is stopped. Use it to see how many {runs} would fail before you enforce.") },
            { value: "enforce", label: "Enforce", help: terms().say("A failing rule stops the step (a {deploy:do}, an image push) or the run at its start, saying why. Someone with the right role can grant a time-limited waiver.") },
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
              help={terms().say("Only deployments to these are judged by the {deploy:do} rules. Nothing ticked: every {environment}. Most teams scope {deploy:do} rules to production.")} />
          ) : <p className="cf-help">{terms().say("{Environments}: only rules judged before {deploy:doing} can be scoped to {environments}, and this policy has none.")}</p>}
          <div className="cf-grid">
            <ListInput label="Branches" value={branches} onChange={setBranches} placeholder="main, release/*"
              help="Only runs of these branches. A run of a tag or a pull request has no branch, so it would not match. Empty: any." />
          </div>
          <PickNames legend="Events" names={EVENTS} value={events} onChange={setEvents} help={terms().say("Only runs started by these. Nothing ticked: every {run}.")} placeholder="" />
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
            help="Lower-case letters, digits and hyphens. It is shown wherever its verdict is." />
          <TextInput label="Description" value={description} onChange={setDescription} wide problems={by.description}
            placeholder="Production deployments need tests and a second person's approval" help="Why the policy exists, in a sentence people will read when it stops them." />
          <DefinedAt overview={overview} value={unit} onChange={setUnit} />
        </div>
      ),
    },
  ];

  return (
    <WizardRunner title="Add a policy" overview={overview}
      intro="Rules runs must meet. Nothing is created until you confirm at the end."
      steps={steps} changes={name.trim() ? realChanges : changes}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <h3 className="h-sm">Next</h3>
          <ul className="cf-next">
            <li><Link href="/policy">See policies and their verdicts</Link></li>
            {mode === "report" && <li>When the warnings look right, set its mode to enforce on <Link href={`/config/policies/${encodeURIComponent(name.trim())}`}>its page</Link>.</li>}
          </ul>
        </>
      )} />
  );
}
