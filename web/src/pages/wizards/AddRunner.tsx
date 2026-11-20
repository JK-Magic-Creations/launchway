// Add a runner: which pool it joins (an existing one or a new one), where it
// runs, then, on Create, the pool (if new) and a join token, the exact
// command for the chosen place, and a wait until the runner connects.

import { useEffect, useState } from "react";
import { ApiError, api, type JoinToken, type Runner, type RunnerPool } from "../../api";
import { type ChangeRequest, type ConfigOverview, type Spec } from "../../configApi";
import { Choices, CheckInput, CopyBlock, ListInput, SelectInput, TextInput } from "../../components/ConfigForm";
import { Link } from "../../components/ui";
import { PickNames, WizardRunner, useNames, useObjects, type StepDef } from "./Wizards";

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
        <p>A runner belongs to a pool. The pool decides what work its runners take: a <strong>build pool</strong> runs the steps of the {proj.plural.toLowerCase()} it serves; a <strong>deploy pool</strong> does deploy and rollback steps for the targets it serves, and nothing else, so it can sit next to a production cluster without ever running anyone's build.</p>
        {poolsError && <p className="cf-problem">The pools could not be listed: {poolsError}</p>}
        <Choices legend="Which pool will the runner join?" value={choice} onChange={setChoice}
          options={[
            ...(pools ?? []).map((p) => ({
              value: "existing:" + p.name, label: `${p.name} (${p.kind} pool)`,
              help: p.kind === "deploy" ? `Deploys to ${p.targets.join(", ") || "no target yet"}.` : `Runs the steps of ${p.projects.length ? p.projects.join(", ") : "every " + proj.singular.toLowerCase()}${p.events.length ? `, for ${p.events.join(", ")} runs` : ""}.`,
              badge: p.require_identity ? undefined : "accepts shared tokens",
            })),
            { value: "new-build", label: "A new build pool", help: `For a team or a kind of machine (arm64, GPUs): runners that run the steps of the ${proj.plural.toLowerCase()} you choose.` },
            { value: "new-deploy", label: "A new deploy pool", help: "Runners that deploy to a target (a cluster), close to it. They hold no one's build steps." },
          ]} />
        {isNew && (
          <div className="cf-sub">
            <div className="cf-grid">
              <TextInput label="Name of the new pool" value={newName} onChange={setNewName} mono required problems={by.name} placeholder={newKind === "build" ? "linux-builders" : "deploy-prod"}
                help="Letters, digits, dots, hyphens and underscores. Pipelines and the runners page show it." />
            </div>
            {newKind === "build" ? (
              <PickNames legend={`${proj.plural} it serves`} names={["*", ...projectNames]} value={projects} onChange={setProjects} problems={by.projects}
                help={`Tick * for every ${proj.singular.toLowerCase()}, or the ones whose steps these runners should take.`} />
            ) : (
              <PickNames legend="Targets it deploys to" names={envTargets} value={targets} onChange={setTargets} problems={by.targets} placeholder="prod-cluster"
                help={envTargets.length ? "The targets of your environments. A deploy step waits until a runner of a pool serving its environment's target takes it." : "No environment has a target yet. Add an environment first, or type the target its environments will have."} />
            )}
            {newKind === "build" && (
              <CheckInput label="Only runners enrolled with a join token" checked={requireIdentity} onChange={setRequireIdentity} problems={by.require_identity}
                help="Recommended. Each runner then has its own key and identity that can be revoked on its own; the shared runner token cannot take this pool's work." />
            )}
            {newKind === "deploy" && <p className="cf-help">A new deploy pool takes only runners enrolled with a join token, so no shared token can deploy.</p>}
          </div>
        )}
      </>
    ),
  };

  const placeStep: StepDef = {
    id: "place", title: "Where it runs",
    render: () => (
      <>
        <Choices legend="Where will the runner run?" value={place} onChange={(v) => { setPlace(v as Place); if (v === "kubernetes") setUses((n) => Math.max(n, 5)); }}
          help="A runner only makes outbound connections to this server: nothing needs to reach it."
          options={[
            { value: "vm", label: "A machine (a server or a virtual machine)", help: "The launchway program runs as a service. Steps run in containers with Podman or Docker if the machine has them. Good for builds that need the machine's own tools or hardware." },
            { value: "container", label: "A container (Podman or Docker)", help: "The runner itself runs in a container, and runs steps inside it. The quickest to try; steps are less isolated from each other than with a container per step." },
            { value: "kubernetes", label: "Kubernetes", help: "Each run gets its own pod. The runner enrols when its pod starts, so the token needs a use for every pod start." },
          ]} />
        <div className="cf-grid">
          <TextInput label="How many runners may enrol with it" type="number" value={String(uses)} onChange={(v) => setUses(Math.max(1, Math.min(10000, Number(v) || 1)))}
            help={place === "kubernetes" ? "Every pod start uses one; give room for restarts and scaling." : "Usually one: a token per runner, so each is traceable to its token."} />
          <SelectInput label="The token is valid for" value={ttl} onChange={setTtl} options={TTLS}
            help="After that, no runner can enrol with it; the runners it enrolled keep working." />
          <ListInput label="Labels (optional)" value={labels} onChange={setLabels} placeholder="arm64, gpu"
            help="Pipelines can ask for runners with these labels, for steps that need special hardware." />
        </div>
      </>
    ),
  };

  return (
    <WizardRunner title="Add a runner" overview={overview}
      intro="Runners do the work of runs. A few questions decide which work this one takes and where it runs; then you get a join token and the exact command."
      steps={[poolStep, placeStep]} changes={changes}
      createLabel={isNew ? "Create the pool and the join token" : "Make the join token"}
      summaryExtra={<p>{isNew ? "Then" : "Create"} makes a join token for pool <span className="mono">{poolName}</span>: for {uses === 1 ? "one runner" : `${uses} runners`}, valid for {TTLS.find((t) => t.value === ttl)?.label}{labels.length ? `, with labels ${labels.join(", ")}` : ""}{place === "kubernetes" ? ", ephemeral (each pod's identity is revoked when it leaves)" : ""}. It is shown once.</p>}
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
          const r = await api.createJoinToken({ pool, max_uses: uses, ttl, labels: labels.length ? labels : undefined, ephemeral, description: "made by the Add a runner wizard" });
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

  if (error) return <div className="callout callout-bad" role="alert"><strong>The join token was not made:</strong> {error}{isNew ? ` The pool ${pool} was created; make a token on the Runners page.` : ""}</div>;
  if (!made) return <p className="faint" aria-live="polite">Making the join token…</p>;
  const origin = location.origin;
  const cmds = commands(origin, made.token, place, kind, labels);
  return (
    <div className="cf-joinwait">
      <div className="callout callout-draft" role="note"><strong>Copy the join token now: it is shown only once</strong> and stored only as a hash. It lets {made.t.max_uses === 1 ? "one runner" : `${made.t.max_uses} runners`} join pool <span className="mono">{pool}</span> until {new Date(made.t.expires_at).toLocaleString()}.</div>
      <CopyBlock title="The join token" text={made.token} />
      {cmds.map((c) => <CopyBlock key={c.title} title={c.title} text={c.text} note={c.note} />)}
      <div className={"cf-waiting" + (runner ? " cf-waiting-ok" : "")} role="status" aria-live="polite">
        {runner ? (
          <><span className="online online-on" aria-hidden="true" /><span><strong>Runner connected:</strong> <span className="mono">{runner.name}</span> joined pool {pool}{runner.executor ? `, running steps with ${runner.executor}` : ""}. <Link href="/runners">See it on the Runners page</Link>.</span></>
        ) : (
          <><span className="cf-spinner" aria-hidden="true" /><span>Waiting for the runner to connect… This page notices it by itself; it can take a minute after the command starts.</span></>
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
      { title: "On the machine, with the launchway program installed", text: `launchway runner --server ${origin} --join ${token}${lab} --data /var/lib/launchway-runner`,
        note: "It enrols once: it makes a key that never leaves the machine and from then on starts without the token. Run the same command (without --join) from a service manager such as systemd to keep it running." },
      ...(kind === "deploy" ? [{ title: "A deploy runner also needs its deploy: section", text: `# /etc/launchway/runner.yaml\nserver: ${origin}\ndeploy:\n  # the targets, Applications and GitOps repositories: see the runner configuration reference`,
        note: "Add --config /etc/launchway/runner.yaml to the command." }] : []),
    ];
  }
  if (place === "container") {
    return [{ title: "Start the runner in a container", text: `podman run -d --name launchway-runner -v lw-runner:/var/lib/launchway-runner \\\n  -e LAUNCHWAY_JOIN=${token} ghcr.io/jk-magic-creations/launchway \\\n  runner --server ${origin} --data /var/lib/launchway-runner --executor host${lab}`,
      note: "docker run works the same way. The volume keeps the runner's key, so it stays the same runner when the container is replaced." }];
  }
  return [
    { title: "1. The join token, as a Secret", text: `kubectl -n launchway create secret generic launchway-runner-join --from-literal=token=${token}` },
    { title: "2. If Launchway runs in this cluster with its Helm chart: runner-join.yaml", text: `runner:\n  join: { existingSecret: launchway-runner-join }${labels.length ? `\n  labels: [${labels.join(", ")}]` : ""}`,
      note: "Then upgrade the release you installed Launchway with, keeping its values: helm upgrade launchway <the chart you installed with> -n launchway --reuse-values -f runner-join.yaml" },
    { title: "Or from any machine that can reach the cluster", text: `launchway runner --server ${origin} --join ${token} --executor kubernetes \\\n  --k8s-context <context> --k8s-namespace launchway-steps${lab}`,
      note: "Step pods run in that namespace, with the machine's kubeconfig." },
  ];
}
