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
import { WordsEditor, wordsProblems, type WordsValue } from "../../components/WordsEditor";
import { withWords } from "../SettingsWords";
import { makeTerms, terms } from "../../terms";
import { PipelineToCommit, RunItNow, useRepoFlow } from "./ConnectRepo";
import { CreatedList, WizardRunner, type StepDef } from "./Wizards";
import { prose } from "../../prose";
const P = await prose("pages.wizards.FirstRun");

const PROVIDERS = [
  { value: "authentik", label: "authentik", issuer: P.t0, groups: "groups" },
  { value: "keycloak", label: "Keycloak", issuer: P.t1, groups: P.t2 },
  { value: "okta", label: "Okta", issuer: P.t3, groups: P.t4 },
  { value: "entra", label: "Microsoft Entra ID", issuer: P.t5, groups: P.t6 },
  { value: "google", label: "Google", issuer: "https://accounts.google.com", groups: P.t7 },
  { value: "other", label: P.t8, issuer: "https://idp.example.com", groups: P.t9 },
];

export function FirstRunWizard({ overview }: { overview: ConfigOverview }) {
  const me = useContext(MeContext);
  const [orgName, setOrgName] = useState(overview.organisation?.display_name ?? "");
  const [words, setWords] = useState<WordsValue>(() => ({ set: terms().set, overrides: { ...terms().overrides } }));
  const wordsAtStart = useState(() => JSON.stringify({ set: terms().set, overrides: terms().overrides }))[0];
  const wt = makeTerms(words);
  // A Squadron is always there now: the wizard asks only for the first one's name (Main when nobody has made one).
  const [unitName, setUnitName] = useState(overview.units_in_use ? "" : "Main");
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
  if (inst && JSON.stringify({ set: words.set, overrides: words.overrides }) !== wordsAtStart) {
    changes.push({ kind: "installation", name: "installation", spec: withWords(inst.spec, words) as Spec, version: inst.version });
  }
  if (org && orgName.trim() && orgName.trim() !== (org.spec.display_name ?? "")) {
    changes.push({ kind: "organisation", name: "default", spec: { ...org.spec, display_name: orgName.trim() }, version: org.version });
  }
  if (unitName.trim() && !overview.units?.some((u) => u.name === unitName.trim())) {
    changes.push({ kind: "unit", name: unitName.trim(), spec: unitDisplay.trim() ? { display_name: unitDisplay.trim() } : {}, version: -1 });
  }
  if (signin === "oidc" && signinCheck.result && signinCheck.result.problems.length === 0) {
    changes.push({ kind: "sign-in", name: "sign-in", version: signinObj, spec: {
      issuer: issuer.trim().replace(/\/+$/, ""), client_id: clientId.trim(), redirect_url: redirect.trim(), client_secret_env: secretEnv.trim(),
      ...(adminGroups.length || adminUsers.length ? { admins: { ...(adminGroups.length ? { groups: adminGroups } : {}), ...(adminUsers.length ? { users: adminUsers } : {}) } } : {}),
    } });
  }
  if (flow.name) changes.push({ ...flow.change, unit: unitName.trim() ? unitName.trim() : flow.change.unit });

  const unitWord = wt.label("unit");

  const wordsStep: StepDef = {
    id: "words", title: "The words Launchway uses",
    fields: [{ path: "terms", match: ["terms", "levels"] }],
    local: () => wordsProblems(words),
    render: () => (
      <>
        <p className="cf-help">{P.t10}</p>
        <WordsEditor value={words} onChange={setWords} />
      </>
    ),
  };

  const namesStep: StepDef = {
    id: "names", title: `Your ${wt.lower("organization")} and its ${wt.pluralLower("unit")}`,
    fields: [{ path: "display_name", match: ["display_name", "organisation"], value: orgName }, { path: "unit", match: ["unit"], value: unitName }],
    local: () => !unitName.trim() && !overview.units_in_use ? `Name the first ${unitWord.toLowerCase()}.` : null,
    render: (by) => (
      <>
        <div className="cf-grid">
          <TextInput label={`Your ${wt.lower("organization")}'s name`} value={orgName} onChange={setOrgName} placeholder="Acme" focusOnMount problems={by.display_name}
            help={P.t11} />
        </div>
        {!overview.units_in_use && (
          <div className="cf-grid">
            <TextInput label={`Name of the first ${unitWord.toLowerCase()}`} value={unitName} onChange={setUnitName} mono placeholder="payments" problems={by.unit}
              help={`A ${unitWord.toLowerCase()} is a named folder of ${wt.pluralLower("project")} with its own people and shared settings. The name is used in URLs and the command line: letters, digits, dots, hyphens and underscores. Your first repository goes into it.`} />
            <TextInput label="Shown as" value={unitDisplay} onChange={setUnitDisplay} placeholder="Payments team" help="How people read it. Optional." />
          </div>
        )}
      </>
    ),
  };

  const signinStep: StepDef = {
    id: "signin", title: "How people sign in",
    local: () => signin === "oidc" && (!issuer.trim() || !clientId.trim()) ? P.t12 : null,
    render: () => (
      <>
        {me?.auth_mode === "oidc" && <div className="callout callout-ok">{P.t13}</div>}
        <Choices legend="How should people sign in?" value={signin} onChange={(v) => setSignin(v as "none" | "oidc")}
          options={[
            { value: "none", label: P.t14,
              help: <><strong>{P.t15}</strong>{P.t16}</> },
            { value: "oidc", label: P.t17,
              help: P.t18 },
          ]} />
        {signin === "oidc" && (
          <div className="cf-sub">
            <Choices legend="Which identity provider?" value={provider} onChange={(v) => { setProvider(v); }}
              options={PROVIDERS.map((p) => ({ value: p.value, label: p.label }))} />
            <p className="cf-help">{P.t19}</p>
            <div className="cf-grid">
              <TextInput label={P.t20} value={issuer} onChange={setIssuer} mono wide placeholder={prov.issuer}
                help={`The issuer URL the provider shows for the application, such as ${prov.issuer}.`} />
              <TextInput label="Client id" value={clientId} onChange={setClientId} mono help={P.t21} />
              <TextInput label="Redirect address" value={redirect} onChange={setRedirect} mono wide
                help={P.t22} />
              <TextInput label={P.t23} value={secretEnv} onChange={setSecretEnv} mono
                help={P.t24} />
              <ListInput label={P.t25} value={adminGroups} onChange={setAdminGroups} placeholder="launchway-admins"
                help={`Group names as the provider sends them (${prov.groups}). They can then give others roles here.`} />
              <ListInput label="Or users" value={adminUsers} onChange={setAdminUsers} placeholder="alice"
                help={P.t26} />
            </div>
            <div className="cf-row">
              <button type="button" className="btn" disabled={!issuer.trim() || signinCheck.state === "checking"} onClick={checkSignin}>
                {signinCheck.state === "checking" ? "Checking…" : "Check with the provider"}</button>
            </div>
            <div aria-live="polite">
              {signinCheck.state === "unavailable" && <p className="faint">{P.t27}</p>}
              {signinCheck.result?.blocked && signinCheck.result.blocked_host && <AllowHost host={signinCheck.result.blocked_host} />}
              {signinCheck.state === "error" && <p className="cf-problem">The check failed: {signinCheck.error}</p>}
              {signinCheck.result && (
                <>
                  {signinCheck.result.problems.length > 0
                    ? <div className="callout callout-bad"><strong>{P.t28}</strong><ul>{signinCheck.result.problems.map((p) => <li key={p}>{p}</li>)}</ul></div>
                    : <div className="callout callout-ok">{P.t29}{signinCheck.result.endpoints.authorization ?? "its address"}. {signinCheck.result.endpoints.groups_claim_hint ?? ""}</div>}
                </>
              )}
            </div>
            <div className="callout callout-info" role="note">
              <strong>{P.t30}</strong> {P.t31}{me?.auth_mode === "oidc" ? " (your identity provider)" : P.t32}, and the saved answers wait.
            </div>
          </div>
        )}
      </>
    ),
  };

  return (
    <WizardRunner title="First-run setup" overview={overview}
      intro={`Four short parts: the words Launchway uses, your ${wt.lower("organization")}, how people sign in, and your first repository. Nothing is created until you confirm at the end.`}
      steps={[wordsStep, namesStep, signinStep, ...flow.steps]} changes={changes}
      summaryExtra={<>
        <PipelineToCommit flow={flow} />
        {signin === "oidc" && <p className="cf-help">{P.t33}</p>}
      </>}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <PipelineToCommit flow={flow} />
          {signin === "oidc" && <div className="callout callout-info">{P.t34}</div>}
          {signin === "none" && <div className="callout callout-draft">{P.t35}</div>}
          <h3 className="h-sm">Next</h3>
          {flow.name && <RunItNow name={flow.name} branch={flow.branch} />}
          <ul className="cf-next">
            <li><Link href="/config/wizard/add-runner">{terms().say("Add a {runner}")}</Link> {terms().say("to take the steps of {runs}")}</li>
            <li><Link href={P.t36}>{terms().say("Add an {environment}")}</Link> {terms().say("to {deploy:do} to")}</li>
            <li><Link href="/config">Back to configuration</Link></li>
          </ul>
        </>
      )} />
  );
}
