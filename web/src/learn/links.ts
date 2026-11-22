// Where the product points into the documentation: each page's "Learn more", each wizard's concept
// page, the pages that explain a wait or a failure, and the pipeline key under the editor's cursor.
// It is data, and a test (links.test.ts) checks that every target is a page and a heading that
// exist in docs/, and that every pattern is a page of the product.

import { learnHref } from "./content";

export interface LearnTarget { slug: string; anchor?: string; label: string }

const t = (label: string, slug: string, anchor?: string): LearnTarget => ({ label, slug, anchor });

/** Learn more, by the pattern of the page in the product (see nav/model.ts). */
export const LEARN_FOR: Record<string, LearnTarget> = {
  "/": t("Learn more about the launch line", "concepts/launch-line"),
  "/projects": t("Learn more about projects and pipelines", "concepts/pipelines-and-runs", "project"),
  "/projects/:name": t("Learn more about projects and pipelines", "concepts/pipelines-and-runs", "project"),
  "/pipelines": t("Learn more about pipelines", "concepts/pipelines-and-runs", "pipeline"),
  "/projects/:name/pipeline": t("Learn more about pipelines", "concepts/pipelines-and-runs", "pipeline"),
  "/projects/:name/pipeline/edit": t("Read about editing pipelines", "guide/editor"),
  "/projects/:name/tests": t("Learn more about {debriefs}", "guide/test-reports"),
  "/projects/:name/security": t("Learn more about security findings", "guide/security-findings"),
  "/projects/:name/caches": t("Learn more about build caches", "guide/build-caches"),
  "/projects/:name/configuration": t("Learn more about configuration as code", "concepts/configuration-as-code"),
  "/runs/:id/configuration/:step": t("Learn more about configuration reports", "guide/ansible"),
  "/projects/:name/variables": t("Learn more about variables", "concepts/variables", "variables-set-at-a-level"),
  "/settings/variables": t("Learn more about variables", "concepts/variables", "variables-set-at-a-level"),
  "/playground": t("Read about writing pipelines", "guide/pipelines"),
  "/activity": t("Learn more about runs", "concepts/pipelines-and-runs", "run"),
  "/activity/approvals": t("Learn more about approvals", "concepts/approvals"),
  "/runs/:id": t("Learn why a {run} waits", "concepts/launch-line", "when-a-change-waits"),
  "/deployments": t("Learn more about {environments} and deployments", "concepts/environments-and-deployments"),
  "/deployments/environments": t("Learn more about {environments}", "concepts/environments-and-deployments", "what-an-environment-holds"),
  "/deployments/environments/:name": t("Learn more about {environments}", "concepts/environments-and-deployments", "what-an-environment-holds"),
  "/deployments/applications": t("Learn more about deployments", "guide/deployments"),
  "/deployments/applications/:name": t("Learn more about deployments", "guide/deployments"),
  "/library": t("Learn more about templates and libraries", "concepts/templates-and-libraries"),
  "/library/starters": t("Learn more about the starters", "guide/starters"),
  "/library/templates": t("Learn more about templates and libraries", "concepts/templates-and-libraries"),
  "/library/templates/:library/:name": t("Learn more about templates", "concepts/templates-and-libraries", "what-exists-today"),
  "/depot": t("Learn more about container images", "guide/images"),
  "/depot/components": t("Learn more about security findings and software bills of materials", "guide/security-findings"),
  "/depot/digest/:digest": t("Learn more about container images", "guide/images"),
  "/settings": t("Start with first-run setup", "howto/setup-first-run"),
  "/settings/runners": t("Learn more about {runners}", "guide/runner-identity"),
  "/settings/access": t("Learn more about sign-in and access", "guide/access"),
  "/settings/access/keys": t("Learn more about sign-in and access", "guide/access"),
  "/settings/access/agents": t("Learn more about connecting an agent", "guide/agents"),
  "/settings/approvals": t("Learn more about approvals", "concepts/approvals", "who-may-decide"),
  "/settings/rules": t("Learn more about policies", "guide/policies"),
  "/settings/audit": t("See every audit action", "reference/events"),
  "/config": t("Learn more about configuration and levels", "guide/configuration", "levels"),
  "/config/:kind": t("Learn more about configuration and levels", "guide/configuration", "levels"),
  "/config/:kind/:name": t("Learn more about configuration objects", "reference/config-objects"),
  "/config/:kind/new": t("Learn more about configuration objects", "reference/config-objects"),
  "/config/wizard/:id": t("Learn more about setting up", "howto/setup-first-run"),
};

/** The concept page behind each wizard. */
export const WIZARD_LEARN: Record<string, LearnTarget> = {
  "first-run": t("Learn more about setting up", "howto/setup-first-run"),
  "connect-repository": t("Learn more about projects and pipelines", "concepts/pipelines-and-runs", "project"),
  "add-runner": t("Learn more about {runners}", "concepts/pipelines-and-runs", "runner"),
  "add-environment": t("Learn more about {environments}", "concepts/environments-and-deployments"),
  "add-policy": t("Learn more about approvals and rules", "concepts/approvals", "rules-not-only-people"),
  "add-health-source": t("Learn more about verifying a deployment", "guide/verification"),
  "add-model": t("Learn more about AI models", "guide/ai"),
};

/** Pages that explain a message the product shows. */
export const EXPLAIN: Record<string, LearnTarget> = {
  waits: t("Why a {run} waits", "concepts/launch-line", "when-a-change-waits"),
  lock: t("Why deployments wait for a lock", "concepts/environments-and-deployments", "one-at-a-time"),
  stopped: t("What to do when a {run} stops", "howto/troubleshooting"),
  clearance: t("How approvals work", "concepts/approvals", "how-it-works"),
};

export function learnFor(pattern: string, path: string): LearnTarget | null {
  if (pattern === "/config/wizard/:id") return WIZARD_LEARN[path.split("/").pop() ?? ""] ?? LEARN_FOR[pattern];
  return LEARN_FOR[pattern] ?? null;
}

export const targetHref = (x: LearnTarget) => learnHref(x.slug, x.anchor);

// ---- the pipeline key under the cursor ----

/** The part of the pipeline reference that explains each key. */
export const KEY_ANCHOR: Record<string, string | undefined> = {
  pipeline: "top-level-keys", on: "triggers", push: "push", pull_request: "pull-request", schedule: "schedule", webhook: "webhook",
  inputs: "inputs", libraries: "libraries", containers: "containers", steps: "steps", runner: "runner", resources: "resources",
  lane: "lane", retry: "retry", when: "when", each: "for-each", secrets: "secret-references", reports: "reports", tests: "reports",
  coverage: "coverage", artifacts: "artifacts", fetch: "fetch", cache: "cache", limits: "limits",
  kind: "step-keys-common-to-all-kinds", needs: "step-keys-common-to-all-kinds", run: "step-keys-common-to-all-kinds",
  image: "step-keys-common-to-all-kinds", env: "step-keys-common-to-all-kinds", container: "containers",
  uses: "a-step-that-uses-a-template", with: "a-step-that-uses-a-template", outputs: "output-run-and-input-values",
};

/** The key a line of a pipeline file sets, such as "needs" for "    needs: [build]", or null. */
export function keyOnLine(line: string): string | null {
  const m = line.match(/^\s*(?:-\s+)?([A-Za-z_][A-Za-z0-9_-]*)\s*:(\s|$)/);
  return m ? m[1] : null;
}

/** The line the cursor is on. */
export function lineAt(text: string, cursor: number): string {
  const start = text.lastIndexOf("\n", cursor - 1) + 1;
  const end = text.indexOf("\n", cursor);
  return text.slice(start, end < 0 ? text.length : end);
}

/** The reference page for a key, or null when the key is not one the reference explains. */
export function referenceFor(key: string | null): LearnTarget | null {
  if (!key || !(key in KEY_ANCHOR)) return null;
  return t(`Reference: ${key}`, "reference/pipeline", KEY_ANCHOR[key]);
}
