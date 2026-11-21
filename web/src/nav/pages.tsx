import { lazy } from "react";
import type { Approvals } from "../live";
import { Empty, Link } from "../components/ui";
import { locate } from "./model";

// Pages load when first opened, so the first page needs less script (the
// run page with its charts is the largest).
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyPage = React.ComponentType<any>;
const lz = (load: () => Promise<Record<string, unknown>>, name: string) =>
  lazy(() => load().then((m) => ({ default: m[name] as AnyPage })));

const HomePage = lz(() => import("../pages/Home"), "HomePage");
const ProjectsPage = lz(() => import("../pages/CodeList"), "CodeListPage");
const ProjectPage = lz(() => import("../pages/RepoPage"), "RepoPage");
const PipelinePage = lz(() => import("../pages/PipelineView"), "PipelineViewPage");
const EditPipelinePage = lz(() => import("../pages/EditPipeline"), "EditPipelinePage");
const ProjectTestsPage = lz(() => import("../pages/ProjectTests"), "ProjectTestsPage");
const ProjectSecurityPage = lz(() => import("../pages/ProjectSecurity"), "ProjectSecurityPage");
const ProjectCachesPage = lz(() => import("../pages/Caches"), "ProjectCachesPage");
const ProjectVariablesPage = lz(() => import("../pages/Variables"), "ProjectVariablesPage");
const SettingsVariablesPage = lz(() => import("../pages/Variables"), "SettingsVariablesPage");
const PlaygroundPage = lz(() => import("../pages/Playground"), "PlaygroundPage");
const RunsPage = lz(() => import("../pages/Activity"), "ActivityPage");
const RunPage = lz(() => import("../pages/Run"), "RunPage");
const ApprovalsPage = lz(() => import("../pages/Decisions"), "DecisionsPage");
const SettingsApprovalsPage = lz(() => import("../pages/SettingsApprovals"), "SettingsApprovalsPage");
const DeploymentsPage = lz(() => import("../pages/DeploymentsOverview"), "DeploymentsOverviewPage");
const EnvironmentsPage = lz(() => import("../pages/Environments"), "EnvironmentsPage");
const EnvironmentPage = lz(() => import("../pages/EnvironmentView"), "EnvironmentViewPage");
const ApplicationsPage = lz(() => import("../pages/ApplicationsList"), "ApplicationsListPage");
const ApplicationPage = lz(() => import("../pages/ApplicationView"), "ApplicationViewPage");
const LibraryPage = lz(() => import("../pages/Library"), "LibraryPage");
const TemplatesPage = lz(() => import("../pages/Library"), "TemplatesPage");
const TemplatePage = lz(() => import("../pages/TemplatePage"), "TemplatePage");
const ImagesPage = lz(() => import("../pages/Images"), "ImagesPage");
const ComponentsPage = lz(() => import("../pages/Components"), "ComponentsPage");
const ImageDetailPage = lz(() => import("../pages/ImageDetail"), "ImageDetailPage");
const PipelinesPage = lz(() => import("../pages/PipelinesList"), "PipelinesPage");
const LearnPage = lz(() => import("../learn/Learn"), "LearnRoot");
const KitPage = lz(() => import("../pages/Kit"), "KitPage");
const ComingPage = lz(() => import("../shell/Coming"), "ComingPage");
const ImagesSwitch = lz(() => import("../pages/ImagesSwitch"), "ImagesSwitch");
const SettingsPage = lz(() => import("../pages/SettingsHome"), "SettingsHomePage");
const AccessPage = lz(() => import("../pages/Settings"), "AccessPage");
const RunnersPage = lz(() => import("../pages/Runners"), "RunnersPage");
const TokensPage = lz(() => import("../pages/Tokens"), "TokensPage");
const RepositoryPage = lz(() => import("../pages/Repository"), "RepositoryPage");
const ImportRepositoriesPage = lz(() => import("../pages/ImportRepositories"), "ImportRepositoriesPage");
const AgentsPage = lz(() => import("../pages/Agents"), "AgentsPage");
const PolicyPage = lz(() => import("../pages/Policy"), "PolicyPage");
const SettingsWordsPage = lz(() => import("../pages/SettingsWords"), "SettingsWordsPage");
const AuditPage = lz(() => import("../pages/Audit"), "AuditPage");
const ConfigHomePage = lz(() => import("../pages/ConfigHome"), "ConfigHomePage");
const ConfigListPage = lz(() => import("../pages/ConfigList"), "ConfigListPage");
const ConfigObjectPage = lz(() => import("../pages/ConfigObject"), "ConfigObjectPage");
const WizardPage = lz(() => import("../pages/wizards/Wizards"), "WizardPage");

/** The page for an address. The key makes a page that shows one thing start afresh for another. */
export function PageFor({ path, approvals }: { path: string; approvals: Approvals }) {
  const at = locate(path);
  if (!at) return <NotFound />;
  const p = at.params;
  switch (at.info.pattern) {
    case "/": return <HomePage />;
    case "/projects": return <ProjectsPage />;
    case "/projects/:name": return <ProjectPage key={p.name} name={p.name} />;
    case "/projects/:name/pipeline": return <PipelinePage key={p.name} name={p.name} />;
    case "/projects/:name/pipeline/edit": return <EditPipelinePage key={p.name} name={p.name} />;
    case "/projects/:name/tests": return <ProjectTestsPage key={p.name} name={p.name} />;
    case "/projects/:name/security": return <ProjectSecurityPage key={p.name} name={p.name} />;
    case "/projects/:name/caches": return <ProjectCachesPage key={p.name} name={p.name} />;
    case "/projects/:name/variables": return <ProjectVariablesPage key={p.name} name={p.name} />;
    case "/repositories/import": return <ImportRepositoriesPage />;
    case "/repositories/:owner/:name": return <RepositoryPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} tab="overview" />;
    case "/repositories/:owner/:name/mirrors": return <RepositoryPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} tab="mirrors" />;
    case "/playground": return <PlaygroundPage />;
    case "/activity": return <RunsPage />;
    case "/activity/approvals": return <ApprovalsPage approvals={approvals} />;
    case "/runs/:id": return <RunPage id={p.id} />;
    case "/deployments": return <DeploymentsPage />;
    case "/deployments/environments": return <EnvironmentsPage />;
    case "/deployments/environments/:name": return <EnvironmentPage key={p.name} name={p.name} />;
    case "/deployments/applications": return <ApplicationsPage />;
    case "/deployments/applications/:name": return <ApplicationPage key={p.name} name={p.name} />;
    case "/library": return <LibraryPage />;
    case "/library/templates": return <TemplatesPage />;
    case "/library/templates/:library/:name": return <TemplatePage key={p.library + "/" + p.name} library={p.library} name={p.name} />;
    case "/images": return <><ImagesSwitch on="images" /><ImagesPage /></>;
    case "/images/components": return <><ImagesSwitch on="components" /><ComponentsPage /></>;
    case "/images/digest/:digest": return <ImageDetailPage key={p.digest} digest={p.digest} />;
    case "/pipelines": return <PipelinesPage />;
    case "/kit": return <KitPage />;
    case "/learn": case "/learn/:a": case "/learn/:a/:b": return <LearnPage path={path} />;
    case "/code/:page": case "/images/:page": case "/traffic": case "/traffic/:page": case "/changes": case "/changes/:page":
    case "/deployments/hosts": case "/deployments/machine-images":
      return <ComingPage path={path.length > 1 ? path.replace(/\/+$/, "") : path} />;
    case "/settings": return <SettingsPage />;
    case "/settings/runners": return <RunnersPage />;
    case "/settings/access": return <AccessPage />;
    case "/settings/access/keys": return <TokensPage />;
    case "/settings/access/agents": return <AgentsPage />;
    case "/settings/approvals": return <SettingsApprovalsPage />;
    case "/settings/rules": return <PolicyPage />;
    case "/settings/audit": return <AuditPage />;
    case "/settings/variables": return <SettingsVariablesPage />;
    case "/settings/words": return <SettingsWordsPage />;
    case "/config": return <ConfigHomePage />;
    case "/config/wizard/:id": return <WizardPage key={p.id} id={p.id} />;
    case "/config/:kind/new": return <ConfigObjectPage key={p.kind + "/new"} kind={p.kind} name="" />;
    case "/config/:kind/:name": return <ConfigObjectPage key={p.kind + "/" + p.name} kind={p.kind} name={p.name} />;
    case "/config/:kind": return <ConfigListPage key={p.kind} kind={p.kind} />;
  }
  return <NotFound />;
}

function NotFound() {
  return <div className="page"><Empty title="Nothing here">This page does not exist. <Link href="/">Go to Home</Link>.</Empty></div>;
}

