// Add a runner: which pool it joins (an existing one or a new one), where it
// runs, then, on Create, the pool (if new) and a join token, the exact
// command for the chosen place, and a wait until the runner connects.

import { useEffect, useState } from "react";
import { ApiError, api, type JoinToken, type Runner, type RunnerPool } from "../../api";
import { type ChangeRequest, type ConfigOverview, type Spec } from "../../configApi";
import { Choices, CheckInput, CopyBlock, ListInput, SelectInput, TextInput } from "../../components/ConfigForm";
import { Link } from "../../components/ui";
import { PickNames, WizardRunner, useNames, useObjects, type StepDef } from "./Wizards";
import { terms } from "../../terms";
import { prose } from "../../prose";
const P = await prose("pages.wizards.AddRunner");

type Place = "vm" | "container" | "kubernetes";

const TTLS = [{ value: "1h", label: "1 hour" }, { value: "24h", label: "1 day" }, { value: "168h", label: "7 days" }];

export function AddRunnerWizard({ overview }: { overview: ConfigOverview }) {
  const proj = overview.levels.project;
  const [pools, setPools] = useState<RunnerPool[] | null>(null);
  const [poolsError, setPoolsError] = useState("");
  const asked = new URLSearchParams(location.search).get("pool") ?? "";
  const [choice, setChoice] = useState<string>(asked ? "existing:" + asked : "");
  const [newName, setNewName] = useState("");
  const [projects, setProjects] = useState<string[]>(["*"]);
  const [targets, setTargets] = useState<string[]>([]);
  const [requireIdentity, setRequireIdentity] = useState(true);
  const [place, setPlace] = useState<Place>("vm");
  const [uses, setUses] = useState(1);
  const [ttl, setTtl] = useState("1h");
  const [labels, setLabels] = useState<string[]>([]);
  const projectNames = useNames("projects");
  const envs = useObjects("environments");
  const envTargets = Array.from(new Set((envs ?? []).map((e) => String((e.spec as Spec).target ?? "")).filter(Boolean)));

  useEffect(() => {
    api.runnerPools().then((p) => { setPools(p.pools); if (!choice) setChoice(p.pools.some((x) => x.kind === "build") ? "existing:" + (p.pools.find((x) => x.kind === "build")!.name) : "new-build"); })
      .catch((e) => { setPools([]); setPoolsError(e instanceof Error ? e.message : String(e)); if (!choice) setChoice("new-build"); });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const isNew = choice.startsWith("new-");
  const newKind = choice === "new-deploy" ? "deploy" : "build";
  const existing = pools?.find((p) => "existing:" + p.name === choice);
  const poolName = isNew ? newName.trim() : existing?.name ?? "";
  const poolKind = isNew ? newKind : existing?.kind ?? "build";

  const spec: Spec = newKind === "build"
    ? { kind: "build", projects, ...(requireIdentity ? { require_identity: true } : {}) }
    : { kind: "deploy", targets, require_identity: true };
  const changes: ChangeRequest[] = isNew && poolName ? [{ kind: "runner-pool", name: poolName, spec, version: -1 }] : [];

  const poolStep: StepDef = {
    id: "pool", title: "Which pool",
    fields: [{ path: "name", match: ["name", "names", "exists", "already"], value: newName }, { path: "projects", value: projects }, { path: "targets", match: ["targets", "target"], value: targets },
      { path: "require_identity" }],
    local: () => !choice ? "Choose a pool." : isNew && !newName.trim() ? "Give the new pool a name." : null,
    render: (by) => (
      <>
        <p>{terms().say(P.t0)} <strong>build pool</strong> runs the steps of the {proj.plural.toLowerCase()} it serves; a <strong>{terms().say("{deploy:do} pool")}</strong> {terms().say(P.t1)}</p>
        {poolsError && <p className="cf-problem">{P.t2}{poolsError}</p>}
        <Choices legend={terms().say(P.t3)} value={choice} onChange={setChoice}
          options={[
            ...(pools ?? []).map((p) => ({
              value: "existing:" + p.name, label: `${p.name} (${p.kind} pool)`,
              help: p.kind === "deploy" ? terms().say(`{Deploy:does} to ${p.targets.join(", ") || "no target yet"}.`) : `Runs the steps of ${p.projects.length ? p.projects.join(", ") : "every " + proj.singular.toLowerCase()}${p.events.length ? `, for ${p.events.join(", ")} runs` : ""}.`,
              badge: p.require_identity ? undefined : "accepts shared tokens",
            })),
            { value: "new-build", label: "A new build pool", help: terms().say(`For a team or a kind of machine (arm64, GPUs): {runners} that run the steps of the ${proj.plural.toLowerCase()} you choose.`) },
            { value: "new-deploy", label: terms().say("A new {deploy:do} pool"), help: terms().say(P.t4) },
          ]} />
        {isNew && (
          <div className="cf-sub">
            <div className="cf-grid">
              <TextInput label="Name of the new pool" value={newName} onChange={setNewName} mono required problems={by.name} placeholder={newKind === "build" ? "linux-builders" : "deploy-prod"}
                help={terms().say(P.t5)} />
            </div>
            {newKind === "build" ? (
              <PickNames legend={`${proj.plural} it serves`} names={["*", ...projectNames]} value={projects} onChange={setProjects} problems={by.projects}
                help={terms().say(`Tick * for every ${proj.singular.toLowerCase()}, or the ones whose steps these {runners} should take.`)} />
            ) : (
              <PickNames legend={terms().say("Targets it {deploy:does} to")} names={envTargets} value={targets} onChange={setTargets} problems={by.targets} placeholder="prod-cluster"
                help={envTargets.length ? terms().say(P.t6) : terms().say(P.t7)} />
            )}
            {newKind === "build" && (
              <CheckInput label={terms().say(P.t8)} checked={requireIdentity} onChange={setRequireIdentity} problems={by.require_identity}
                help={terms().say(P.t9)} />
            )}
            {newKind === "deploy" && <p className="cf-help">{terms().say(P.t10)}</p>}
          </div>
        )}
      </>
    ),
  };

  const placeStep: StepDef = {
    id: "place", title: "Where it runs",
    render: () => (
      <>
        <Choices legend={terms().say("Where will the {runner} run?")} value={place} onChange={(v) => { setPlace(v as Place); if (v === "kubernetes") setUses((n) => Math.max(n, 5)); }}
          help={terms().say(P.t11)}
          options={[
            { value: "vm", label: P.t12, help: P.t13 },
            { value: "container", label: P.t14, help: terms().say(P.t15) },
            { value: "kubernetes", label: "Kubernetes", help: terms().say(P.t16) },
          ]} />
        <div className="cf-grid">
          <TextInput label={terms().say(P.t17)} type="number" value={String(uses)} onChange={(v) => setUses(Math.max(1, Math.min(10000, Number(v) || 1)))}
            help={place === "kubernetes" ? P.t18 : terms().say(P.t19)} />
          <SelectInput label="The token is valid for" value={ttl} onChange={setTtl} options={TTLS}
            help={terms().say(P.t20)} />
          <ListInput label="Labels (optional)" value={labels} onChange={setLabels} placeholder="arm64, gpu"
            help={terms().say(P.t21)} />
        </div>
      </>
    ),
  };

  return (
    <WizardRunner title={terms().say("Add a {runner}")} overview={overview}
      intro={terms().say(P.t22)}
      steps={[poolStep, placeStep]} changes={changes}
      createLabel={isNew ? P.t23 : "Make the join token"}
      summaryExtra={<p>{isNew ? "Then" : "Create"} makes a join token for pool <span className="mono">{poolName}</span>: for {uses === 1 ? terms().say("one {runner}") : `${uses} runners`}, valid for {TTLS.find((t) => t.value === ttl)?.label}{labels.length ? `, with labels ${labels.join(", ")}` : ""}{place === "kubernetes" ? P.t24 : ""}. It is shown once.</p>}
      done={() => <JoinAndWait pool={poolName} kind={poolKind} uses={uses} ttl={ttl} labels={labels} place={place} ephemeral={place === "kubernetes"} isNew={isNew} />}  />
  );
}

/** Makes the join token (once), shows the command, and waits for the runner. */
function JoinAndWait({ pool, kind, uses, ttl, labels, place, ephemeral, isNew }: {
  pool: string; kind: string; uses: number; ttl: string; labels: string[]; place: Place; ephemeral: boolean; isNew: boolean;
}) {
  const [made, setMade] = useState<{ token: string; t: JoinToken } | null>(null);
  const [error, setError] = useState("");
  const [runner, setRunner] = useState<Runner | null>(null);

  useEffect(() => {
    let stop = false;
    (async () => {
      // A new pool reaches every server within seconds; until it has, the server does not know it.
      for (let i = 0; i < 12 && !stop; i++) {
        try {
          const r = await api.createJoinToken({ pool, max_uses: uses, ttl, labels: labels.length ? labels : undefined, ephemeral, description: terms().say(P.t25) });
          if (!stop) setMade({ token: r.token, t: r.join_token });
          return;
        } catch (e) {
          if (isNew && e instanceof ApiError && (e.status === 404 || e.status === 400) && i < 11) { await new Promise((ok) => window.setTimeout(ok, 1000)); continue; }
          if (!stop) setError(e instanceof Error ? e.message : String(e));
          return;
        }
      }
    })();
    return () => { stop = true; };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!made || runner) return;
    const look = () => api.runners(true).then((rs) => {
      const r = rs.find((x) => x.identity?.join_token === made.t.id || (x.pool === pool && (x.identity?.enrolled_at ?? 0) >= made.t.created_at));
      if (r) setRunner(r);
    }).catch(() => {});
    look();
    const t = window.setInterval(look, 3000);
    return () => window.clearInterval(t);
  }, [made, runner, pool]);

  if (error) return <div className="callout callout-bad" role="alert"><strong>The join token was not made:</strong> {error}{isNew ? terms().say(` The pool ${pool} was created; make a token on the {Runners} page.`) : ""}</div>;
  if (!made) return <p className="faint" aria-live="polite">Making the join token…</p>;
  const origin = location.origin;
  const cmds = commands(origin, made.token, place, kind, labels);
  return (
    <div className="cf-joinwait">
      <div className="callout callout-draft" role="note"><strong>{P.t26}</strong> {P.t27}{made.t.max_uses === 1 ? terms().say("one {runner}") : `${made.t.max_uses} runners`} join pool <span className="mono">{pool}</span> until {new Date(made.t.expires_at).toLocaleString()}.</div>
      <CopyBlock title="The join token" text={made.token} />
      {cmds.map((c) => <CopyBlock key={c.title} title={c.title} text={c.text} note={c.note} />)}
      <div className={"cf-waiting" + (runner ? " cf-waiting-ok" : "")} role="status" aria-live="polite">
        {runner ? (
          <><span className="online online-on" aria-hidden="true" /><span><strong>{terms().say("{Runner} connected:")}</strong> <span className="mono">{runner.name}</span> joined pool {pool}{runner.executor ? `, running steps with ${runner.executor}` : ""}. <Link href="/runners">{terms().say("See it on the {Runners} page")}</Link>.</span></>
        ) : (
          <><span className="cf-spinner" aria-hidden="true" /><span>{terms().say(P.t28)}</span></>
        )}
      </div>
    </div>
  );
}

type Cmd = { title: string; text: string; note?: string };

/** The commands for each place, with the flags `launchway runner` has
 * (cmd/launchway/main.go) and the chart's runner.join value. */
function commands(origin: string, token: string, place: Place, kind: string, labels: string[]): Cmd[] {
  const lab = labels.length ? ` --labels ${labels.join(",")}` : "";
  if (place === "vm") {
    return [
      { title: P.t29, text: `launchway runner --server ${origin} --join ${token}${lab} --data /var/lib/launchway-runner`,
        note: P.t30 },
      ...(kind === "deploy" ? [{ title: terms().say(P.t31), text: terms().say(`# /etc/launchway/runner.yaml\nserver: ${origin}\ndeploy:\n  # the targets, Applications and GitOps repositories: see the {runner} configuration reference`),
        note: P.t32 }] : []),
    ];
  }
  if (place === "container") {
    return [{ title: terms().say(P.t33), text: `podman run -d --name launchway-runner -v lw-runner:/var/lib/launchway-runner \\\n  -e LAUNCHWAY_JOIN=${token} ghcr.io/jk-magic-creations/launchway \\\n  runner --server ${origin} --data /var/lib/launchway-runner --executor host${lab}`,
      note: P.t34 }];
  }
  return [
    { title: P.t35, text: `kubectl -n launchway create secret generic launchway-runner-join --from-literal=token=${token}` },
    { title: P.t36, text: terms().say(`{runner}:\n  join: { existingSecret: launchway-runner-join }${labels.length ? `\n  labels: [${labels.join(", ")}]` : ""}`),
      note: P.t37 },
    { title: P.t38, text: `launchway runner --server ${origin} --join ${token} --executor kubernetes \\\n  --k8s-context <context> --k8s-namespace launchway-steps${lab}`,
      note: P.t39 },
  ];
}
