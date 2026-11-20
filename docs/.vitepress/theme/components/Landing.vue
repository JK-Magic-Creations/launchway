<script setup lang="ts">
import { withBase, useData } from 'vitepress'
import { ref } from 'vue'

const { theme } = useData()
const demoUrl = (theme.value as any).demoUrl as string
const pending: string[] = (theme.value as any).pendingPages || []
// A link to a page: pages not written yet lead to the "being written" page.
const page = (path: string) => {
  const [p, hash] = path.split('#')
  if (pending.includes(p)) return withBase('/pending.html')
  return withBase(p + '.html' + (hash ? '#' + hash : ''))
}

// The delivery flow drawn in the hero. "status" is honest: every stage drawn
// here is available on the main branch.
const flow = [
  { id: 'commit', label: 'Commit', note: 'Git push, pull request, schedule or webhook', status: 'available' },
  { id: 'build', label: 'Build', note: 'Steps in containers or Kubernetes pods', status: 'available' },
  { id: 'test', label: 'Test', note: 'Thirteen report formats, coverage, flaky tests', status: 'available' },
  { id: 'scan', label: 'Scan', note: 'SARIF findings, SBOMs, image scans', status: 'available' },
  { id: 'deploy', label: 'Deploy', note: 'GitOps with Argo CD, canary with Argo Rollouts', status: 'available' },
  { id: 'verify', label: 'Verify', note: 'Metrics and logs judged after a deploy; abort, roll back or promote', status: 'available' },
]
const active = ref(-1)

type Tile = { title: string; text: string; link: string; status: 'available' | 'in progress' | 'planned' }
const tiles: Tile[] = [
  { title: 'Durable runs', text: 'Every change is an event in the database. Runs survive crashes and resume from the step that failed.', link: '/start/architecture#the-event-sourced-engine', status: 'available' },
  { title: 'High availability', text: 'Several servers share one PostgreSQL database with no leader. Losing a server loses no work.', link: '/guide/high-availability', status: 'available' },
  { title: 'Outbound-only runners', text: 'Runners connect out to the server. Steps run in Kubernetes pods or containers, with services beside them.', link: '/howto/connect-runner', status: 'available' },
  { title: 'Secrets stay with runners', text: 'Pipelines hold references. Runners read values from Vault, OpenBao, files or the environment and mask them.', link: '/guide/secrets', status: 'available' },
  { title: 'Test and security reports', text: 'Test results in 13 formats, coverage, flaky tests, SARIF findings that are new or fixed, SBOMs and a component search.', link: '/guide/test-reports', status: 'available' },
  { title: 'Signed images', text: 'Rootless BuildKit builds, vulnerability scans with a policy, provenance and cosign-compatible signatures.', link: '/guide/images', status: 'available' },
  { title: 'GitOps deployments', text: 'Write-back to Git, Argo CD sync and health, canary promote, abort and retry, rollback by revert.', link: '/guide/deployments', status: 'available' },
  { title: 'Policy gates', text: 'Typed rules at run start, before deploys and before image pushes, enforced or reported, with waivers.', link: '/guide/policies', status: 'available' },
  { title: 'Sign-in and audit', text: 'OpenID Connect, roles per project and environment, expiring API tokens, an append-only audit trail.', link: '/guide/access', status: 'available' },
  { title: 'Shared libraries', text: 'Templates and scripts in a Git repository, pinned by tag, with typed parameters and no expression language.', link: '/guide/libraries', status: 'available' },
  { title: 'Verification with metrics', text: 'A step that checks metrics and logs after a deploy, with optional AI help to explain results.', link: '/guide/verification', status: 'available' },
  { title: 'Runner identities and pools', text: 'Runners enrol with join tokens and their own keys, serve only the projects of their pool, and limit what they accept.', link: '/guide/runner-identity', status: 'available' },
  { title: 'Built-in artifact storage', text: 'Artifacts and hosted reports are kept in the database: several servers, nothing else to run.', link: '/artifacts', status: 'available' },
  { title: 'An endpoint for AI agents', text: 'MCP tools to write and check pipelines, start and watch runs and diagnose failures, with a token\'s rights.', link: '/guide/agents', status: 'available' },
  { title: 'Hosted control plane', text: 'Organisations and a management API for everything.', link: '/roadmap', status: 'planned' },
]

const shots = [
  { src: '/images/landing/run.png', alt: 'A run in the Launchway web UI: the steps drawn as a graph, with the log of the selected step below.', caption: 'A run: the graph of steps and the log of the selected one.' },
  { src: '/images/landing/tests.png', alt: 'The Tests tab of a run: totals, a pass rate, failed tests first, and a flaky test marked.', caption: 'Test results, with failures first and flaky tests marked.' },
  { src: '/images/landing/security.png', alt: 'The Security tab of a run: findings by severity from several scanners, marked new or still there.', caption: 'Security findings from any SARIF scanner, new and fixed across runs.' },
  { src: '/images/landing/approvals.png', alt: 'The Approvals page listing runs waiting for a decision, with the question each one asks.', caption: 'Approvals waiting for a person.' },
]
</script>

<template>
  <div class="lw-landing">
    <section class="lw-hero" aria-labelledby="lw-hero-title">
      <div class="lw-hero-text">
        <p class="lw-eyebrow">Open source · AGPL-3.0 · early software</p>
        <h1 id="lw-hero-title">Build, test and deliver software on infrastructure you control.</h1>
        <p class="lw-lead">
          Launchway is a build and delivery platform for teams. It runs your pipelines on your own
          servers or Kubernetes, keeps a durable record of every run, and takes changes to production
          through tests, scans, approvals and policy checks.
        </p>
        <div class="lw-actions">
          <a class="lw-btn lw-btn-primary" :href="page('/start/quick-start')">Quick start</a>
          <a class="lw-btn" :href="page('/demo')">Try the demo</a>
          <a class="lw-btn" :href="withBase('/roadmap.html')" target="_self">Roadmap</a>
          <a class="lw-btn" href="https://github.com/JK-Magic-Creations/launchway">GitHub</a>
          <a v-if="demoUrl" class="lw-btn" :href="demoUrl">Hosted demo</a>
        </div>
      </div>

      <figure class="lw-flow" aria-labelledby="lw-flow-caption">
        <svg viewBox="0 64 640 112" role="img" aria-labelledby="lw-flow-title lw-flow-desc">
          <title id="lw-flow-title">The delivery flow</title>
          <desc id="lw-flow-desc">A change moves from commit to build, test, scan, deploy and verify.
            All six are available today.</desc>
          <path class="lw-track" d="M 40 105 H 600" />
          <path class="lw-pulse" d="M 40 105 H 600" />
          <g v-for="(s, i) in flow" :key="s.id"
             :transform="`translate(${40 + i * 112}, 105)`"
             class="lw-node" :class="[s.status, { on: active === i }]"
             tabindex="0" :aria-label="`${s.label}: ${s.note}`"
             @mouseenter="active = i" @mouseleave="active = -1"
             @focus="active = i" @blur="active = -1">
            <circle r="24" />
            <text class="lw-node-num" dy="5">{{ i + 1 }}</text>
            <text class="lw-node-label" y="50">{{ s.label }}</text>
          </g>
        </svg>
        <figcaption id="lw-flow-caption" class="lw-flow-note" aria-live="polite">
          <template v-if="active >= 0">
            <strong>{{ flow[active].label }}.</strong> {{ flow[active].note }}
          </template>
          <template v-else>From commit to a verified deployment. Hover or tab through the steps.</template>
        </figcaption>
      </figure>
    </section>

    <section class="lw-section" aria-labelledby="lw-features">
      <h2 id="lw-features">What it does</h2>
      <p class="lw-legend">
        <span class="lw-chip available">available</span> on the main branch today ·
        <span class="lw-chip progress">in progress</span> being built ·
        <span class="lw-chip planned">planned</span> designed, not started
      </p>
      <div class="lw-tiles">
        <a v-for="t in tiles" :key="t.title" class="lw-tile" :href="t.link === '/roadmap' ? withBase('/roadmap.html') : page(t.link)" :target="t.link === '/roadmap' ? '_self' : undefined">
          <span class="lw-chip" :class="t.status === 'available' ? 'available' : t.status === 'in progress' ? 'progress' : 'planned'">{{ t.status }}</span>
          <h3>{{ t.title }}</h3>
          <p>{{ t.text }}</p>
        </a>
      </div>
    </section>

    <section class="lw-section" aria-labelledby="lw-see">
      <h2 id="lw-see">See it running</h2>
      <figure class="lw-shot">
        <img :src="withBase(shots[0].src)" :alt="shots[0].alt" loading="lazy" />
        <figcaption>{{ shots[0].caption }}</figcaption>
      </figure>
      <div class="lw-shots">
        <figure v-for="s in shots.slice(1)" :key="s.src" class="lw-shot">
          <img :src="withBase(s.src)" :alt="s.alt" loading="lazy" />
          <figcaption>{{ s.caption }}</figcaption>
        </figure>
      </div>
      <p><a :href="page('/demo')">See all the screenshots in the tour</a>, or run the demo yourself with the
        <a :href="page('/start/quick-start')">quick start</a>.</p>
    </section>

    <section class="lw-section lw-readers" aria-labelledby="lw-who">
      <h2 id="lw-who">Start where you are</h2>
      <div class="lw-tiles lw-tiles-4">
        <a class="lw-tile" :href="page('/paths/developer')"><h3>Developer</h3><p>Write a pipeline, run it on your laptop, read its tests and findings.</p></a>
        <a class="lw-tile" :href="page('/paths/operator')"><h3>Operator</h3><p>Install on one server or Kubernetes, connect runners, sign-in, backups.</p></a>
        <a class="lw-tile" :href="page('/paths/security-reviewer')"><h3>Security reviewer</h3><p>The trust model, its current limits, hardening, controls and evidence.</p></a>
        <a class="lw-tile" :href="page('/paths/decision-maker')"><h3>Decision maker</h3><p>What Launchway is for, what exists today, what is planned.</p></a>
      </div>
    </section>
  </div>
</template>

<style scoped>
.lw-landing { max-width: 1152px; margin: 0 auto; padding: 32px 24px 64px; }
.lw-hero { display: grid; grid-template-columns: 1.05fr 1fr; gap: 40px; align-items: center; padding: 24px 0 40px; }
.lw-eyebrow { color: var(--vp-c-text-2); font-size: 14px; margin: 0 0 8px; }
.lw-hero h1 { font-size: 40px; line-height: 1.15; font-weight: 700; letter-spacing: -0.02em; margin: 0 0 16px; color: var(--vp-c-text-1); }
.lw-lead { font-size: 18px; line-height: 1.6; color: var(--vp-c-text-2); margin: 0 0 24px; }
.lw-actions { display: flex; flex-wrap: wrap; gap: 10px; }
.lw-btn { display: inline-block; padding: 8px 18px; border-radius: 20px; border: 1px solid var(--vp-c-border); color: var(--vp-c-text-1); font-weight: 600; font-size: 14px; text-decoration: none; background: var(--vp-c-bg-soft); transition: border-color .2s, background .2s; }
.lw-btn:hover { border-color: var(--vp-c-brand-1); }
.lw-btn-primary { background: var(--vp-c-brand-3); border-color: var(--vp-c-brand-3); color: var(--vp-c-white); }
.lw-btn-primary:hover { background: var(--vp-c-brand-2); }
.lw-btn:focus-visible, .lw-tile:focus-visible, .lw-node:focus-visible { outline: 2px solid var(--vp-c-brand-1); outline-offset: 3px; }

.lw-flow { margin: 0; background: var(--vp-c-bg-soft); border: 1px solid var(--vp-c-divider); border-radius: 12px; padding: 16px 8px 12px; }
.lw-flow svg { width: 100%; height: auto; display: block; }
.lw-track { stroke: var(--vp-c-divider); stroke-width: 4; fill: none; stroke-linecap: round; }
.lw-pulse { stroke: var(--vp-c-brand-1); stroke-width: 4; fill: none; stroke-linecap: round; stroke-dasharray: 60 600; animation: lw-run 4.5s linear infinite; }
@keyframes lw-run { from { stroke-dashoffset: 60; } to { stroke-dashoffset: -600; } }
.lw-node { cursor: default; outline: none; }
.lw-node circle { fill: var(--vp-c-bg); stroke: var(--vp-c-brand-1); stroke-width: 3; transition: transform .2s; }
.lw-node.partial circle { stroke-dasharray: 6 4; stroke: var(--vp-c-warning-1); }
.lw-node.on circle { fill: var(--vp-c-brand-soft); transform: scale(1.12); }
.lw-node-num { text-anchor: middle; font-weight: 700; font-size: 17px; fill: var(--vp-c-text-1); }
.lw-node-label { text-anchor: middle; font-size: 17px; font-weight: 600; fill: var(--vp-c-text-1); }
.lw-flow-note { min-height: 3em; padding: 4px 12px 0; font-size: 14px; color: var(--vp-c-text-2); text-align: center; }
@media (prefers-reduced-motion: reduce) { .lw-pulse { animation: none; stroke-dasharray: none; opacity: .5; } }

.lw-section { padding: 40px 0 0; border-top: 1px solid var(--vp-c-divider); margin-top: 24px; }
.lw-section h2 { font-size: 26px; font-weight: 700; margin: 0 0 12px; color: var(--vp-c-text-1); border: 0; padding: 0; }
.lw-legend { color: var(--vp-c-text-2); font-size: 14px; margin: 0 0 20px; }
.lw-tiles { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; }
.lw-tiles-4 { grid-template-columns: repeat(4, 1fr); }
.lw-tile { display: block; padding: 18px 20px; border: 1px solid var(--vp-c-divider); border-radius: 12px; background: var(--vp-c-bg-soft); color: inherit; text-decoration: none; transition: border-color .2s; }
.lw-tile:hover { border-color: var(--vp-c-brand-1); }
.lw-tile h3 { margin: 8px 0 6px; font-size: 17px; font-weight: 600; color: var(--vp-c-text-1); }
.lw-tile p { margin: 0; font-size: 14px; line-height: 1.55; color: var(--vp-c-text-2); }
.lw-chip { display: inline-block; font-size: 12px; font-weight: 600; padding: 1px 9px; border-radius: 10px; border: 1px solid; }
.lw-chip.available { color: var(--vp-c-green-1, #18794e); border-color: currentColor; }
.lw-chip.progress { color: var(--vp-c-warning-1); border-color: currentColor; }
.lw-chip.planned { color: var(--vp-c-text-2); border-color: currentColor; border-style: dashed; }
.lw-shot { margin: 0 0 24px; }
.lw-shots { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; }
@media (max-width: 960px) { .lw-shots { grid-template-columns: 1fr; } }
.lw-shot img { width: 100%; border-radius: 10px; border: 1px solid var(--vp-c-divider); box-shadow: 0 8px 30px rgba(0,0,0,.12); }
.lw-shot figcaption { font-size: 14px; color: var(--vp-c-text-2); margin-top: 8px; }

@media (max-width: 960px) {
  .lw-hero { grid-template-columns: 1fr; }
  .lw-tiles, .lw-tiles-4 { grid-template-columns: repeat(2, 1fr); }
}
@media (max-width: 600px) {
  .lw-landing { padding: 16px 16px 48px; }
  .lw-hero h1 { font-size: 30px; }
  .lw-tiles, .lw-tiles-4 { grid-template-columns: 1fr; }
}
</style>
