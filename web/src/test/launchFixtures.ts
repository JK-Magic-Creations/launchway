import type { DeploymentView, RunDetail } from "../api";
import type { LNode, TreeView } from "../launchApi";

const T0 = 1_760_000_000_000;
const n = (o: Partial<LNode> & Pick<LNode, "id" | "kind" | "name">): LNode => ({ namespace: "shop", health: "Healthy", ...o });

/** A canary rollout of shop, 20% to the new version, paused; with `broken` its canary pod crash-loops. */
export function launchTree(broken = false): TreeView {
  const nodes: LNode[] = [
    n({ id: "Rollout/shop/shop", kind: "Rollout", name: "shop", section: "workloads", health: "Suspended", message: "CanaryPauseStep", change: "changed", replicas: { desired: 4, ready: 4, available: 4, updated: 1 } }),
    n({ id: "ReplicaSet/shop/shop-aaa", kind: "ReplicaSet", name: "shop-aaa", parent: "Rollout/shop/shop", role: "stable", revision: "6", hash: "aaa", replicas: { desired: 3, ready: 3, available: 3, updated: 3 } }),
    n({ id: "ReplicaSet/shop/shop-bbb", kind: "ReplicaSet", name: "shop-bbb", parent: "Rollout/shop/shop", role: "canary", revision: "7", hash: "bbb", change: "new", replicas: { desired: 1, ready: broken ? 0 : 1, available: broken ? 0 : 1, updated: 1 }, health: broken ? "Progressing" : "Healthy" }),
    ...[1, 2, 3].map((i) => n({ id: `Pod/shop/shop-aaa-${i}`, kind: "Pod", name: `shop-aaa-${i}`, parent: "ReplicaSet/shop/shop-aaa", containers: [{ name: "app", image: "shop:aaa", ready: true, restarts: 0, state: "running" }] })),
    broken
      ? n({ id: "Pod/shop/shop-bbb-1", kind: "Pod", name: "shop-bbb-1", parent: "ReplicaSet/shop/shop-bbb", health: "Degraded", change: "new", message: "container app: CrashLoopBackOff",
        containers: [{ name: "app", image: "shop:bbb", ready: false, restarts: 5, state: "waiting", reason: "CrashLoopBackOff", previous: true }],
        not_ready: { why: "container app is waiting: CrashLoopBackOff; event BackOff: Back-off restarting failed container app", last_logs: ["panic: database password is empty"] },
        events: [{ type: "Warning", reason: "BackOff", message: "Back-off restarting failed container app", count: 9, last: T0 - 5000 }] })
      : n({ id: "Pod/shop/shop-bbb-1", kind: "Pod", name: "shop-bbb-1", parent: "ReplicaSet/shop/shop-bbb", change: "new", containers: [{ name: "app", image: "shop:bbb", ready: true, restarts: 0, state: "running" }] }),
    n({ id: "Service/shop/shop-stable", kind: "Service", name: "shop-stable", section: "network", role: "stable", ports: ["80/tcp"], message: "ClusterIP; selects shop-aaa", selects: ["ReplicaSet/shop/shop-aaa"] }),
    n({ id: "Service/shop/shop-canary", kind: "Service", name: "shop-canary", section: "network", role: "canary", ports: ["80/tcp"], message: "ClusterIP; selects shop-bbb", selects: ["ReplicaSet/shop/shop-bbb"] }),
    n({ id: "HTTPRoute/shop/shop", kind: "HTTPRoute", name: "shop", section: "network", message: "hosts shop.example.com", weights: [{ backend: "shop-stable", weight: 80 }, { backend: "shop-canary", weight: 20 }] }),
    n({ id: "ConfigMap/shop/shop-config", kind: "ConfigMap", name: "shop-config", section: "config", keys: ["LOG_LEVEL", "db.conf"], data: { LOG_LEVEL: "info", "db.conf": "host=db\npassword=***\n" }, used_by: ["Rollout/shop/shop"] }),
    n({ id: "Secret/shop/shop-creds", kind: "Secret", name: "shop-creds", section: "config", keys: ["password", "user"], message: "type Opaque; values are never shown" }),
    n({ id: "HorizontalPodAutoscaler/shop/shop", kind: "HorizontalPodAutoscaler", name: "shop", section: "scaling", message: "min 2, max 8; now 4, wants 4" }),
    n({ id: "AnalysisRun/shop/shop-bbb-2-3", kind: "AnalysisRun", name: "shop-bbb-2-3", parent: "Rollout/shop/shop", health: "Progressing", message: "Running" }),
    n({ id: "Job/shop/shop-bbb-2-3-push", kind: "Job", name: "shop-bbb-2-3-push", parent: "AnalysisRun/shop/shop-bbb-2-3", health: "Progressing" }),
    n({ id: "Pod/shop/shop-bbb-2-3-push-x", kind: "Pod", name: "shop-bbb-2-3-push-x", parent: "Job/shop/shop-bbb-2-3-push", containers: [{ name: "push", image: "tester:1", ready: true, restarts: 0, state: "running" }] }),
    n({ id: "ConfigMap/shop/shop-old-config", kind: "ConfigMap", name: "shop-old-config", section: "config", change: "leftover", message: "not in what the application renders now" }),
  ];
  return {
    tree: {
      application: "shop", namespaces: ["shop"], at: Date.now(), since: T0 - 120_000, nodes, notes: [],
      rollout: { name: "shop", namespace: "shop", kind: "Rollout", strategy: "canary", phase: "Paused", message: "CanaryPauseStep", paused: true, aborted: false, current: 2, desired_weight: 20, actual_weight: 20, actual_source: "traffic router",
        services: { stable: "shop-stable", canary: "shop-canary" }, pause_started: Date.now() - 30_000,
        steps: [
          { index: 0, kind: "setWeight", label: "Send 10% of traffic to the new version", weight: 10, state: "done" },
          { index: 1, kind: "setWeight", label: "Send 20% of traffic to the new version", weight: 20, state: "done" },
          { index: 2, kind: "pause", label: "Pause until someone promotes", state: "current" },
          { index: 3, kind: "analysis", label: "Analysis: success-rate", state: "next" },
          { index: 4, kind: "setWeight", label: "Send 50% of traffic to the new version", weight: 50, state: "next" },
          { index: 5, kind: "pause", label: "Pause 5m", seconds: 300, state: "next" },
        ],
        analysis: [{ name: "shop-bbb-2-3", phase: "Running", jobs: ["Job/shop/shop-bbb-2-3-push"], metrics: [{ name: "success-rate", provider: "prometheus", query: "sum(rate(http_requests_total{code!~\"5..\"}[1m]))", success: "result[0] >= 0.95", phase: "Running",
          points: [{ at: T0 - 20_000, phase: "Successful", value: "[0.99]" }, { at: T0 - 10_000, phase: "Failed", value: "[0.91]" }] }] }] },
    },
    actions: [{ action: "pause", ok: true }, { action: "resume", ok: false, why: "it is not paused by a person" }, { action: "restart", ok: true }, { action: "scale", ok: false, why: "the autoscaler shop manages the replica count" }],
    run: "run_7", deployment: "dep_1", project: "shop-api", environment: "prod", application: "shop", target: "homelab", closed: false, protected: true, can_logs: true, can_control: true,
  };
}

export function launchDeployment(): DeploymentView {
  return {
    id: "dep_1", run_id: "run_7", run_number: 7, project: "shop-api", step: "deploy-prod", environment: "prod", service: "shop", target: "homelab", application: "shop", mode: "deploy",
    version: "1.4.0", previous_version: "1.3.2", changed: true, phase: "rolling_out", detail: "", opened_at: Date.now() - 600_000, holding: true, commands: [],
  } as unknown as DeploymentView;
}

export function launchRun(base: RunDetail): RunDetail {
  return { ...base, id: "run_7", deployments: [launchDeployment()] } as RunDetail;
}
