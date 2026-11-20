// The first-run setup: what to call things, how people sign in, and the
// first project. The sign-in answers are checked with the identity provider
// (the server's guarded check) and saved as the sign-in settings; the client
// secret is never asked for, only the name of the variable that holds it.
// Saving does not switch sign-in on: that needs a successful test sign-in by
// the administrator, which this version cannot do yet, and the step says so.

import { useContext, useEffect, useState } from "react";
import { MeContext } from "../../auth";
import { configApi, notOffered, type ChangeRequest, type ConfigOverview, type SignInCheck, type Spec } from "../../configApi";
import { Choices, ListInput, TextInput } from "../../components/ConfigForm";
import { AllowHost } from "../../components/AllowHost";
import { Link } from "../../components/ui";
import { PipelineToCommit, RunItNow, useRepoFlow } from "./ConnectRepo";
import { CreatedList, WizardRunner, type StepDef } from "./Wizards";

const PROVIDERS = [
  { value: "authentik", label: "authentik", issuer: "https://auth.example.com/application/o/launchway/", groups: "groups" },
  { value: "keycloak", label: "Keycloak", issuer: "https://sso.example.com/realms/acme", groups: "groups (add a group mapper to the client)" },
  { value: "okta", label: "Okta", issuer: "https://acme.okta.com/oauth2/default", groups: "groups (add a groups claim to the authorization server)" },
  { value: "entra", label: "Microsoft Entra ID", issuer: "https://login.microsoftonline.com/<tenant id>/v2.0", groups: "groups (group ids unless you configure names)" },
  { value: "google", label: "Google", issuer: "https://accounts.google.com", groups: "Google sends no groups: name the first administrators by user" },
  { value: "other", label: "Another OpenID Connect provider", issuer: "https://idp.example.com", groups: "groups, or whatever claim your provider puts groups in" },
];

export function FirstRunWizard({ overview }: { overview: ConfigOverview }) {
  const me = useContext(MeContext);
  const lv = overview.levels;
  const [orgName, setOrgName] = useState(overview.organisation?.display_name ?? "");
  const [naming, setNaming] = useState<"usual" | "own">("usual");
  const [names, setNames] = useState({ projectS: lv.project.singular, projectP: lv.project.plural, unitS: lv.unit.singular, unitP: lv.unit.plural, orgS: lv.organisation.singular });
  const [useUnits, setUseUnits] = useState<"no" | "yes">(overview.units_in_use ? "yes" : "no");
  const [unitName, setUnitName] = useState("");
  const [unitDisplay, setUnitDisplay] = useState("");
  const [signin, setSignin] = useState<"none" | "oidc">(me?.auth_mode === "oidc" ? "oidc" : "none");
  const [provider, setProvider] = useState("authentik");
  const [issuer, setIssuer] = useState("");
  const [clientId, setClientId] = useState("launchway");
  const [redirect, setRedirect] = useState(`${location.origin}/auth/callback`);
  const [secretEnv, setSecretEnv] = useState("LAUNCHWAY_OIDC_SECRET");
  const [adminGroups, setAdminGroups] = useState<string[]>([]);
  const [adminUsers, setAdminUsers] = useState<string[]>([]);
  const [signinCheck, setSigninCheck] = useState<{ state: "idle" | "checking" | "unavailable" | "error"; result?: SignInCheck; error?: string }>({ state: "idle" });
  const [inst, setInst] = useState<{ spec: Spec; version: number } | null>(null);
  const [signinObj, setSigninObj] = useState<number>(-1);
  const [org, setOrg] = useState<{ spec: Spec; version: number } | null>(null);
  const flow = useRepoFlow(overview);

  // What exists is kept: the levels are set on top of the installation's other settings.
  useEffect(() => {
    configApi.get("installation", "installation").then((o) => setInst({ spec: o.spec, version: o.version })).catch(() => setInst({ spec: {}, version: -1 }));
    configApi.get("sign-in", "sign-in").then((o) => setSigninObj(o.version > 0 ? o.version : -1)).catch(() => setSigninObj(-1));
    configApi.get("organisation", "default").then((o) => setOrg({ spec: o.spec, version: o.version })).catch(() => setOrg({ spec: {}, version: -1 }));
  }, []);

  const prov = PROVIDERS.find((p) => p.value === provider)!;
  const checkSignin = async () => {
    setSigninCheck({ state: "checking" });
    try {
      const r = await configApi.checkSignIn({ issuer: issuer.trim(), client_id: clientId.trim(), redirect_url: redirect.trim(), client_secret_env: secretEnv.trim() || undefined,
        admin_groups: adminGroups.length ? adminGroups : undefined, admin_users: adminUsers.length ? adminUsers : undefined });
      setSigninCheck({ state: "idle", result: r });
    } catch (e) {
      if (notOffered(e)) setSigninCheck({ state: "unavailable" });
      else setSigninCheck({ state: "error", error: e instanceof Error ? e.message : String(e) });
    }
  };

  const changes: ChangeRequest[] = [];
  if (inst) {
    const levels = naming === "own" || useUnits === "yes" ? {
      organisation: { singular: names.orgS.trim() },
      unit: { singular: names.unitS.trim(), plural: names.unitP.trim() },
      project: { singular: names.projectS.trim(), plural: names.projectP.trim() },
    } : null;
    if (levels && naming === "own") {
      const cur = (inst.spec.levels as Spec | undefined) ?? {};
      changes.push({ kind: "installation", name: "installation", spec: { ...inst.spec, levels: { ...cur, ...levels } }, version: inst.version });
    }
  }
  if (org && orgName.trim() && orgName.trim() !== (org.spec.display_name ?? "")) {
    changes.push({ kind: "organisation", name: "default", spec: { ...org.spec, display_name: orgName.trim() }, version: org.version });
  }
  if (useUnits === "yes" && unitName.trim() && !overview.units?.some((u) => u.name === unitName.trim())) {
    changes.push({ kind: "unit", name: unitName.trim(), spec: unitDisplay.trim() ? { display_name: unitDisplay.trim() } : {}, version: -1 });
  }
  if (signin === "oidc" && signinCheck.result && signinCheck.result.problems.length === 0) {
    changes.push({ kind: "sign-in", name: "sign-in", version: signinObj, spec: {
      issuer: issuer.trim().replace(/\/+$/, ""), client_id: clientId.trim(), redirect_url: redirect.trim(), client_secret_env: secretEnv.trim(),
      ...(adminGroups.length || adminUsers.length ? { admins: { ...(adminGroups.length ? { groups: adminGroups } : {}), ...(adminUsers.length ? { users: adminUsers } : {}) } } : {}),
    } });
  }
  if (flow.name) changes.push({ ...flow.change, unit: useUnits === "yes" && unitName.trim() ? unitName.trim() : flow.change.unit });

  const unitWord = (naming === "own" ? names.unitS : lv.unit.singular) || "Unit";
  const projWord = (naming === "own" ? names.projectS : lv.project.singular) || "Project";

  const namesStep: StepDef = {
    id: "names", title: "What to call things",
    fields: [{ path: "levels", match: ["levels"] }, { path: "display_name", match: ["display_name", "organisation"], value: orgName }, { path: "unit", match: ["unit"], value: unitName }],
    local: () => useUnits === "yes" && !unitName.trim() && !overview.units_in_use ? `Name the first ${unitWord.toLowerCase()}, or choose not to use them.` : null,
    render: (by) => (
      <>
        <div className="cf-grid">
          <TextInput label="Your organisation's name" value={orgName} onChange={setOrgName} placeholder="Acme" focusOnMount problems={by.display_name}
            help="Shown at the top of the configuration and where it helps people know where they are. Optional." />
        </div>
        <Choices legend="What should the things Launchway manages be called?" value={naming} onChange={(v) => setNaming(v as "usual" | "own")}
          help="Launchway has levels: the installation, an organisation that owns everything, optional groups, and the projects (one per repository). Their names are only words on the screen; you can change them later."
          options={[
            { value: "usual", label: `Keep these names: ${lv.organisation.singular}, ${lv.unit.plural}, ${lv.project.plural}`, help: "Nothing to decide now." },
            { value: "own", label: "Our own words", help: "For example Company, Teams and Services, so the screens use your organisation's language." },
          ]} />
        {naming === "own" && (
          <fieldset className="cf-section">
            <legend className="cf-section-title">Names</legend>
            <div className="cf-grid">
              <TextInput label="The organisation is a…" value={names.orgS} onChange={(v) => setNames({ ...names, orgS: v })} problems={by.levels} help="Such as Company." />
              <TextInput label="One repository is a…" value={names.projectS} onChange={(v) => setNames({ ...names, projectS: v })} help="Such as Service, Application or Project." />
              <TextInput label="Several are…" value={names.projectP} onChange={(v) => setNames({ ...names, projectP: v })} help="The plural." />
              {useUnits === "yes" && <TextInput label="A group of them is a…" value={names.unitS} onChange={(v) => setNames({ ...names, unitS: v })} help="Such as Team or Department." />}
              {useUnits === "yes" && <TextInput label="Several groups are…" value={names.unitP} onChange={(v) => setNames({ ...names, unitP: v })} help="The plural." />}
            </div>
          </fieldset>
        )}
        <Choices legend={`Do you want to group ${(naming === "own" ? names.projectP : lv.project.plural).toLowerCase()}?`} value={useUnits} onChange={(v) => setUseUnits(v as "no" | "yes")}
          options={[
            { value: "no", label: "No, not now", help: `Every ${projWord.toLowerCase()} shares the organisation's settings. The simplest start; you can add groups any time and the screens then show them.` },
            { value: "yes", label: `Yes, into ${(naming === "own" ? names.unitP : lv.unit.plural).toLowerCase()}`,
              help: `Each team gets its own roles, libraries, policies, environments, runner pools and secret stores, which apply only to its ${(naming === "own" ? names.projectP : lv.project.plural).toLowerCase()}. One level, never nested.` },
          ]} />
        {useUnits === "yes" && !overview.units_in_use && (
          <div className="cf-grid">
            <TextInput label={`Name of the first ${unitWord.toLowerCase()}`} value={unitName} onChange={setUnitName} mono placeholder="payments" problems={by.unit}
              help="Used in URLs and the command line: letters, digits, dots, hyphens and underscores. Your first repository goes into it." />
            <TextInput label="Shown as" value={unitDisplay} onChange={setUnitDisplay} placeholder="Payments team" help="How people read it. Optional." />
          </div>
        )}
      </>
    ),
  };

  const signinStep: StepDef = {
    id: "signin", title: "How people sign in",
    local: () => signin === "oidc" && (!issuer.trim() || !clientId.trim()) ? "Give the provider's address and the client id, or choose no sign-in for now." : null,
    render: () => (
      <>
        {me?.auth_mode === "oidc" && <div className="callout callout-ok">People already sign in with your identity provider on this server. You can skip this step.</div>}
        <Choices legend="How should people sign in?" value={signin} onChange={(v) => setSignin(v as "none" | "oidc")}
          options={[
            { value: "none", label: "No sign-in, for trying Launchway out",
              help: <><strong>Anyone who can reach this server has full access</strong>, as one shared user. Fine on your own laptop; never on a network others can reach.</> },
            { value: "oidc", label: "Our company's identity provider (OpenID Connect)",
              help: "People sign in with the accounts they already have; their groups decide what they may do. Needed for anything shared." },
          ]} />
        {signin === "oidc" && (
          <div className="cf-sub">
            <Choices legend="Which identity provider?" value={provider} onChange={(v) => { setProvider(v); }}
              options={PROVIDERS.map((p) => ({ value: p.value, label: p.label }))} />
            <p className="cf-help">First make an application (an OpenID Connect client) for Launchway in the provider, with the redirect address below, and note its client id and secret.</p>
            <div className="cf-grid">
              <TextInput label="The provider's address (issuer)" value={issuer} onChange={setIssuer} mono wide placeholder={prov.issuer}
                help={`The issuer URL the provider shows for the application, such as ${prov.issuer}.`} />
              <TextInput label="Client id" value={clientId} onChange={setClientId} mono help="The application's client id in the provider." />
              <TextInput label="Redirect address" value={redirect} onChange={setRedirect} mono wide
                help="Where the provider sends people back after they sign in. Register exactly this in the provider. It is this page's address plus /auth/callback." />
              <TextInput label="Environment variable that will hold the client secret" value={secretEnv} onChange={setSecretEnv} mono
                help="The secret is never typed here or stored by Launchway: you put it in this variable on every server." />
              <ListInput label="Groups whose members are the first administrators" value={adminGroups} onChange={setAdminGroups} placeholder="launchway-admins"
                help={`Group names as the provider sends them (${prov.groups}). They can then give others roles here.`} />
              <ListInput label="Or users" value={adminUsers} onChange={setAdminUsers} placeholder="alice"
                help="User names of the first administrators, if you prefer to name people." />
            </div>
            <div className="cf-row">
              <button type="button" className="btn" disabled={!issuer.trim() || signinCheck.state === "checking"} onClick={checkSignin}>
                {signinCheck.state === "checking" ? "Checking…" : "Check with the provider"}</button>
            </div>
            <div aria-live="polite">
              {signinCheck.state === "unavailable" && <p className="faint">This check is not available on this server, so the sign-in answers cannot be checked or saved here.</p>}
              {signinCheck.result?.blocked && signinCheck.result.blocked_host && <AllowHost host={signinCheck.result.blocked_host} />}
              {signinCheck.state === "error" && <p className="cf-problem">The check failed: {signinCheck.error}</p>}
              {signinCheck.result && (
                <>
                  {signinCheck.result.problems.length > 0
                    ? <div className="callout callout-bad"><strong>The provider's answers have problems:</strong><ul>{signinCheck.result.problems.map((p) => <li key={p}>{p}</li>)}</ul></div>
                    : <div className="callout callout-ok">The provider answers: sign-in at {signinCheck.result.endpoints.authorization ?? "its address"}. {signinCheck.result.endpoints.groups_claim_hint ?? ""}</div>}
                </>
              )}
            </div>
            <div className="callout callout-info" role="note">
              <strong>What happens with these answers.</strong> They are saved as the sign-in settings and kept with the history of every change. Saving does not switch sign-in on:
              that needs you to sign in successfully with them first, so that nobody is locked out, and this version cannot do that test sign-in yet. Until it can, this server keeps the
              sign-in it started with{me?.auth_mode === "oidc" ? " (your identity provider)" : " (none: anyone who can reach it has full access)"}, and the saved answers wait.
            </div>
          </div>
        )}
      </>
    ),
  };

  return (
    <WizardRunner title="First-run setup" overview={overview}
      intro="Three short parts: what to call things, how people sign in, and your first repository. Nothing is created until you confirm at the end."
      steps={[namesStep, signinStep, ...flow.steps]} changes={changes}
      summaryExtra={<>
        <PipelineToCommit flow={flow} />
        {signin === "oidc" && <p className="cf-help">The sign-in answers are saved, not switched on: that waits for a test sign-in, which this version cannot do yet.</p>}
      </>}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <PipelineToCommit flow={flow} />
          {signin === "oidc" && <div className="callout callout-info">Your sign-in answers are saved. They are not switched on: that needs a successful test sign-in with them, which this version cannot do yet, so this server keeps the sign-in it started with.</div>}
          {signin === "none" && <div className="callout callout-draft">Sign-in is still off: anyone who can reach this server has full access. Run the first-run setup again, or follow the sign-in guide, before others can reach it.</div>}
          <h3 className="h-sm">Next</h3>
          {flow.name && <RunItNow name={flow.name} branch={flow.branch} />}
          <ul className="cf-next">
            <li><Link href="/config/wizard/add-runner">Add a runner</Link> to take the steps of runs</li>
            <li><Link href="/config/wizard/add-environment">Add an environment</Link> to deploy to</li>
            <li><Link href="/config">Back to configuration</Link></li>
          </ul>
        </>
      )} />
  );
}
