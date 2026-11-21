import { defineConfig } from 'vitepress'
import GithubSlugger from 'github-slugger'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, posix } from 'node:path'

const docsDir = join(dirname(fileURLToPath(import.meta.url)), '..')

// Pages that are planned but not written yet are shown in the sidebar as
// "being written", and links to them lead to pending.md, so that the site can
// be published while the documentation is completed.
const pageExists = (link: string) => {
  const p = link.replace(/^\//, '').replace(/#.*$/, '').replace(/\.html$/, '')
  return existsSync(join(docsDir, p.endsWith('/') || p === '' ? p + 'index.md' : p + '.md'))
}

// GitHub-compatible heading anchors, so a link like page.md#some-heading works
// both on github.com and on the site.
const slugger = new GithubSlugger()
const slugify = (s: string) => {
  slugger.reset()
  return slugger.slug(s)
}

const repo = 'https://github.com/JK-Magic-Creations/launchway'

// Site settings that change per deployment. DOCS_BASE overrides the base path
// (GitHub Pages serves the site under /launchway/).
export const site = {
  base: process.env.DOCS_BASE || '/launchway/',
  // A hosted demo instance, when one exists. Leave empty until then: the
  // "Try the demo" page and the landing page link to the local demo instead.
  demoUrl: '',
}

type Item = { text: string; link?: string; target?: string; items?: Item[]; collapsed?: boolean }
const markPending = (items: Item[]): Item[] =>
  items.map((i) =>
    i.items
      ? { ...i, items: markPending(i.items) }
      : i.link && !i.target && !pageExists(i.link)
        ? { text: `${i.text} (being written)`, link: '/pending' }
        : i,
  )

export default defineConfig({
  title: 'Launchway',
  description: 'Launchway: build and delivery for teams, run on your own servers or Kubernetes.',
  lang: 'en-GB',
  base: site.base,
  cleanUrls: false,
  lastUpdated: false,
  // Relative links into docs/public are rewritten to the published files (see below).
  ignoreDeadLinks: [/(^|\/)public\//],
  srcExclude: ['**/node_modules/**', 'README.md', 'package.json'],
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${site.base}logo.svg` }],
    ['meta', { name: 'theme-color', content: '#2f5bd3' }],
  ],
  markdown: {
    anchor: { slugify },
    headers: { level: [2, 3], slugify } as any,
    theme: { light: 'github-light', dark: 'github-dark' },
    // ```mermaid blocks render in the browser (theme/components/Mermaid.vue), in
    // the site's light or dark theme. GitHub renders the same blocks natively.
    config(md) {
      // Links to files in docs/public (written relative, so they work on GitHub)
      // point at the published copy on the site, loaded as a whole page.
      const linkOpen = md.renderer.rules.link_open
      md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
        const href = tokens[idx].attrGet('href') || ''
        const m = href.match(/^(?:\.\.?\/)*public\/(.+)$/)
        if (m) {
          tokens[idx].attrSet('href', site.base + m[1])
          tokens[idx].attrSet('target', '_self')
        }
        return linkOpen ? linkOpen(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options)
      }
      // Links to planned pages that do not exist yet go to pending.md.
      const linkOpen2 = md.renderer.rules.link_open!
      md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
        const href = tokens[idx].attrGet('href') || ''
        if (/^[^:#]+\.md(#.*)?$/.test(href) && env?.relativePath) {
          const target = posix.normalize(posix.join(posix.dirname(env.relativePath), href.replace(/#.*$/, '')))
          if (!existsSync(join(docsDir, target))) {
            const up = '../'.repeat(env.relativePath.split('/').length - 1)
            tokens[idx].attrSet('href', up + 'pending.md')
          }
        }
        return linkOpen2(tokens, idx, options, env, self)
      }
      // [text](app:/route) points at a page of the product itself. In the product's Learn area it
      // is a link; on this site, where there is no product to open, it is the text with a hint.
      const linkOpen3 = md.renderer.rules.link_open!
      md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
        const href = tokens[idx].attrGet('href') || ''
        if (href.startsWith('app:') && !process.env.LAUNCHWAY_LEARN) {
          tokens[idx].tag = 'span'
          tokens[idx].attrs = [['class', 'app-route'], ['title', 'In Launchway: ' + href.slice(4)]]
          for (let j = idx + 1; j < tokens.length; j++) if (tokens[j].type === 'link_close') { tokens[j].tag = 'span'; break }
          return self.renderToken(tokens, idx, options)
        }
        return linkOpen3(tokens, idx, options, env, self)
      }
      const fence = md.renderer.rules.fence!
      md.renderer.rules.fence = (tokens, idx, options, env, self) => {
        const token = tokens[idx]
        if (token.info.trim() === 'mermaid') {
          return `<Mermaid code="${encodeURIComponent(token.content)}" />`
        }
        return fence(tokens, idx, options, env, self)
      }
    },
  },
  vite: {
    // The site must build without network access once dependencies are installed.
    build: { chunkSizeWarningLimit: 4000 },
  },
  themeConfig: {
    logo: '/logo.svg',
    siteTitle: 'Launchway',
    demoUrl: site.demoUrl,
    // Pages the landing page links to that are not written yet.
    pendingPages: ['/demo', '/howto/connect-runner', '/start/quick-start', '/paths/developer', '/paths/operator',
      '/paths/security-reviewer', '/paths/decision-maker', '/guide/high-availability', '/guide/secrets',
      '/guide/test-reports', '/guide/images', '/guide/deployments', '/guide/policies', '/guide/access',
      '/guide/libraries', '/start/architecture'].filter((l) => !pageExists(l)),
    nav: [
      { text: 'Start here', link: '/start/overview' },
      { text: 'Tutorials', link: '/tutorials/' },
      { text: 'Guides', link: '/guide/' },
      { text: 'How-to', link: '/howto/' },
      { text: 'Reference', link: '/reference/' },
      { text: 'Security', link: '/security/' },
      {
        text: 'Roadmap',
        items: [
          { text: 'Overview', link: '/roadmap.html', target: '_self' },
          { text: 'Milestone 1: Host and run', link: '/roadmap/milestone-1' },
          { text: 'Milestone 2: Pipeline power', link: '/roadmap/milestone-2' },
          { text: 'Milestone 3: Delivery depth', link: '/roadmap/milestone-3' },
          { text: 'Milestone 4: Registry', link: '/roadmap/milestone-4' },
          { text: 'Milestone 5: Software graph', link: '/roadmap/milestone-5' },
          { text: 'Milestone 6: AI helper', link: '/roadmap/milestone-6' },
          { text: 'Milestone 7: Hosted service', link: '/roadmap/milestone-7' },
          { text: 'Beyond delivery', link: '/roadmap/beyond' },
          { text: 'As a list', link: '/roadmap-list' },
        ],
      },
    ],
    sidebar: markPending([
      {
        text: 'Start here',
        items: [
          { text: 'What Launchway is', link: '/start/overview' },
          { text: 'Concepts', link: '/start/concepts' },
          { text: 'Architecture', link: '/start/architecture' },
          { text: 'Quick start', link: '/start/quick-start' },
          { text: 'Try the demo', link: '/demo' },
        ],
      },
      {
        text: 'Concepts',
        collapsed: false,
        items: [
          { text: 'Start here: a walkthrough', link: '/learn/start-here' },
          { text: 'Overview', link: '/learn/concepts/' },
          { text: 'The launch line', link: '/learn/concepts/launch-line' },
          { text: 'Projects, pipelines, steps and runs', link: '/learn/concepts/pipelines-and-runs' },
          { text: 'Templates, libraries and flight plans', link: '/learn/concepts/templates-and-libraries' },
          { text: 'Variables', link: '/learn/concepts/variables' },
          { text: 'Environments and deployments', link: '/learn/concepts/environments-and-deployments' },
          { text: 'Approvals', link: '/learn/concepts/approvals' },
          { text: 'High availability', link: '/learn/concepts/high-availability' },
        ],
      },
      {
        text: 'Paths by role',
        collapsed: true,
        items: [
          { text: 'Developer', link: '/paths/developer' },
          { text: 'Operator and platform engineer', link: '/paths/operator' },
          { text: 'Security reviewer', link: '/paths/security-reviewer' },
          { text: 'Business and decision maker', link: '/paths/decision-maker' },
        ],
      },
      {
        text: 'Tutorials',
        collapsed: false,
        items: [
          { text: 'Overview', link: '/tutorials/' },
          { text: 'First pipeline in 10 minutes', link: '/tutorials/first-pipeline' },
          { text: 'Tests and reports', link: '/tutorials/tests-and-reports' },
          { text: 'Security scans', link: '/tutorials/security-scans' },
          { text: 'Build and sign an image', link: '/tutorials/build-and-sign-image' },
          { text: 'Deploy with approval and rollback', link: '/tutorials/deploy-with-approval' },
          { text: 'Policy gates and waivers', link: '/tutorials/policy-gates' },
          { text: 'Pull-request runs', link: '/tutorials/pull-requests' },
          { text: 'Templates from a library', link: '/tutorials/library-templates' },
        ],
      },
      {
        text: 'User guide',
        collapsed: true,
        items: [
          { text: 'Overview', link: '/guide/' },
          { text: 'Pipelines', link: '/guide/pipelines' },
          { text: 'Triggers', link: '/guide/triggers' },
          { text: 'Queues and limits', link: '/guide/queues' },
          { text: 'Events', link: '/guide/events' },
          { text: 'Event sources', link: '/guide/event-sources' },
          { text: 'Triggers on results', link: '/guide/result-triggers' },
          { text: 'Editing pipelines', link: '/guide/editor' },
          { text: 'Built-in accounts', link: '/guide/built-in-accounts' },
          { text: 'Shared libraries', link: '/guide/libraries' },
          { text: 'Approvals and re-runs', link: '/guide/approvals-and-reruns' },
          { text: 'Test reports', link: '/guide/test-reports' },
          { text: 'Artifacts', link: '/guide/artifacts' },
          { text: 'Variables at levels', link: '/guide/variables' },
          { text: 'Build caches', link: '/guide/build-caches' },
          { text: 'The built-in registry', link: '/guide/registry' },
          { text: 'Where artifacts are kept', link: '/artifacts' },
          { text: 'Security findings and SBOMs', link: '/guide/security-findings' },
          { text: 'Container images', link: '/guide/images' },
          { text: 'Deployments', link: '/guide/deployments' },
          { text: 'GitOps write-back', link: '/guide/gitops-write-back' },
          { text: 'Policies', link: '/guide/policies' },
          { text: 'Verifying a deployment', link: '/guide/verification' },
          { text: 'Notifications', link: '/guide/notifications' },
          { text: 'AI models', link: '/guide/ai' },
          { text: 'Git over SSH', link: '/guide/git-over-ssh' },
          { text: 'Browsing code', link: '/guide/browsing-code' },
          { text: 'Searching code', link: '/guide/searching-code' },
          { text: 'Editing in the browser', link: '/guide/editing-in-the-browser' },
          { text: 'Pull requests and code review', link: '/guide/pull-requests' },
          { text: 'Pull-request trust', link: '/guide/pull-request-trust' },
          { text: 'Keeping dependencies up to date', link: '/guide/dependency-updates' },
          { text: 'Webhooks and events', link: '/guide/webhooks' },
          { text: 'Secrets', link: '/guide/secrets' },
          { text: 'Encryption at rest', link: '/guide/encryption-at-rest' },
          { text: 'Upgrading', link: '/guide/upgrading' },
          { text: 'Configuration and levels', link: '/guide/configuration' },
          { text: 'The words Launchway uses', link: '/guide/words' },
          { text: 'Sign-in and access', link: '/guide/access' },
          { text: 'Runners on other machines', link: '/guide/runner-identity' },
          { text: 'Agents (MCP)', link: '/guide/agents' },
          { text: 'Accessibility', link: '/guide/accessibility' },
          { text: 'Running several servers', link: '/guide/high-availability' },
        ],
      },
      {
        text: 'How-to guides',
        collapsed: true,
        items: [
          { text: 'Overview', link: '/howto/' },
          { text: 'Install in ten minutes', link: '/howto/install-in-ten-minutes' },
          { text: 'Install on a laptop', link: '/howto/install-laptop' },
          { text: 'Install on one server', link: '/howto/install-single-server' },
          { text: 'Install on Kubernetes (HA)', link: '/howto/install-kubernetes' },
          { text: 'Install on Amazon EKS', link: '/howto/install-eks' },
          { text: 'Install on Google GKE', link: '/howto/install-gke' },
          { text: 'First-run setup', link: '/howto/setup-first-run' },
          { text: 'Connect a repository', link: '/howto/connect-repository' },
          { text: 'Add a runner', link: '/howto/add-runner' },
          { text: 'Add an environment', link: '/howto/add-environment' },
          { text: 'Add a policy', link: '/howto/add-policy' },
          { text: 'Add a health source', link: '/howto/add-health-source' },
          { text: 'Add an AI model', link: '/howto/add-ai-model' },
          { text: 'Connect a runner', link: '/howto/connect-runner' },
          { text: 'Sign-in with OIDC and roles', link: '/howto/oidc-and-roles' },
          { text: 'Secret stores', link: '/howto/secrets' },
          { text: 'Backup, restore, upgrade', link: '/howto/backup-restore-upgrade' },
          { text: 'Self-deploy', link: '/howto/self-deploy' },
          { text: 'Monitor Launchway', link: '/howto/monitoring' },
          { text: 'Troubleshooting', link: '/howto/troubleshooting' },
        ],
      },
      {
        text: 'Reference',
        collapsed: true,
        items: [
          { text: 'Overview', link: '/reference/' },
          { text: 'Pipeline file', link: '/reference/pipeline' },
          { text: 'Server configuration', link: '/reference/server-config' },
          { text: 'Runner configuration', link: '/reference/runner-config' },
          { text: 'Helm values', link: '/reference/helm-values' },
          { text: 'Command line', link: '/reference/cli' },
          { text: 'HTTP API', link: '/reference/http-api' },
          { text: 'Dependency updates', link: '/reference/dependency-updates' },
          { text: 'Configuration objects', link: '/reference/config-objects' },
          { text: 'Interface parity', link: '/reference/interface-parity' },
          { text: 'Accessibility conformance', link: '/reference/accessibility-conformance' },
          { text: 'Archive format', link: '/reference/archive-format' },
          { text: 'Events and audit actions', link: '/reference/events' },
          { text: 'Roles and permissions', link: '/reference/roles' },
          { text: 'Policy rules', link: '/reference/policy-rules' },
          { text: 'Environment variables', link: '/reference/environment-variables' },
        ],
      },
      {
        text: 'Security',
        collapsed: true,
        items: [
          { text: 'Overview', link: '/security/' },
          { text: 'Trust model and threats', link: '/security/trust-model' },
          { text: 'Hardening checklist', link: '/security/hardening' },
          { text: 'How Launchway is scanned', link: '/security/how-launchway-is-scanned' },
          { text: 'Threat model', link: '/security/threat-model' },
          { text: 'Supply chain', link: '/security/supply-chain' },
          { text: 'Verifying downloads', link: '/security/verify-downloads' },
          { text: 'Controls and compliance', link: '/security/controls-and-compliance' },
          { text: 'Reporting a vulnerability', link: '/security/reporting' },
        ],
      },
      {
        text: 'Roadmap',
        collapsed: false,
        items: [
          { text: 'Overview (big picture)', link: '/roadmap.html', target: '_self' },
          { text: 'Milestone 1: Host and run', link: '/roadmap/milestone-1' },
          { text: 'Milestone 2: Pipeline power', link: '/roadmap/milestone-2' },
          { text: 'Milestone 3: Delivery depth', link: '/roadmap/milestone-3' },
          { text: 'Milestone 4: Registry', link: '/roadmap/milestone-4' },
          { text: 'Milestone 5: Software graph', link: '/roadmap/milestone-5' },
          { text: 'Milestone 6: AI helper', link: '/roadmap/milestone-6' },
          { text: 'Milestone 7: Hosted service', link: '/roadmap/milestone-7' },
          { text: 'Beyond delivery', link: '/roadmap/beyond' },
          { text: 'Git hosting and review', link: '/roadmap/git-hosting' },
          { text: 'Pipelines and flow control', link: '/roadmap/pipelines' },
          { text: 'The built-in registry', link: '/roadmap/registry' },
          { text: 'Delivery and hosts', link: '/roadmap/delivery' },
          { text: 'Traffic, protection, reliability', link: '/roadmap/traffic' },
          { text: 'Software graph and patching', link: '/roadmap/software-graph' },
          { text: 'AI as an optional helper', link: '/roadmap/ai' },
          { text: 'Hosted service and sandboxes', link: '/roadmap/hosted-service' },
          { text: 'The guided interface', link: '/roadmap/guided-interface' },
          { text: 'Roadmap, as a list', link: '/roadmap-list' },
        ],
      },
      {
        text: 'More',
        collapsed: true,
        items: [
          { text: 'Glossary', link: '/glossary' },
          { text: 'Vocabulary rules', link: '/reference/vocabulary' },
          { text: 'FAQ', link: '/faq' },
          { text: 'How Launchway is built', link: '/contributing/codebase' },
          { text: 'Writing the docs', link: '/contributing/docs' },
          { text: 'Docs style guide', link: '/contributing/style' },
          { text: 'Design principles for the interface', link: '/contributing/design-principles' },
          { text: 'Writing a migration', link: '/development/migrations' },
          { text: 'Quality bar for the API, MCP and CLI', link: '/development/interface-quality' },
        ],
      },
    ]),
    socialLinks: [{ icon: 'github', link: repo }],
    search: { provider: 'local' },
    editLink: { pattern: `${repo}/edit/main/docs/:path`, text: 'Edit this page' },
    outline: { level: [2, 3] },
    footer: {
      message: 'AGPL-3.0 licence. Features marked planned or in progress are not available yet.',
      copyright: 'Launchway contributors',
    },
  },
})
