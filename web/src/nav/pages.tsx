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

const SshKeysPage = lz(() => import("../pages/SshKeys"), "SshKeysPage");
const RepositoriesPage = lz(() => import("../pages/Repositories"), "RepositoriesPage");
const OwnerPage = lz(() => import("../pages/Repositories"), "OwnerPage");
const HomePage = lz(() => import("../pages/Home"), "HomePage");
const ProjectsPage = lz(() => import("../pages/CodeList"), "CodeListPage");
const ProjectPage = lz(() => import("../pages/RepoPage"), "RepoPage");
const PipelinePage = lz(() => import("../pages/PipelineView"), "PipelineViewPage");
const EditPipelinePage = lz(() => import("../pages/EditPipeline"), "EditPipelinePage");
const ImportPipelinePage = lz(() => import("../pages/import/ImportWizard"), "default");
const ProjectTestsPage = lz(() => import("../pages/ProjectTests"), "ProjectTestsPage");
const ProjectSecurityPage = lz(() => import("../pages/ProjectSecurity"), "ProjectSecurityPage");
const ProjectStandardsPage = lz(() => import("../pages/Standards"), "ProjectStandardsPage");
const SettingsStandardsPage = lz(() => import("../pages/Standards"), "SettingsStandardsPage");
const ProjectCachesPage = lz(() => import("../pages/Caches"), "ProjectCachesPage");
const ProjectConfigurationPage = lz(() => import("../pages/Configuration"), "ProjectConfigurationPage");
const ConfigReportPage = lz(() => import("../pages/ConfigReport"), "ConfigReportPage");
const ProjectVariablesPage = lz(() => import("../pages/Variables"), "ProjectVariablesPage");
const ProjectTogglesPage = lz(() => import("../pages/Toggles"), "ProjectTogglesPage");
const ToggleDetailPage = lz(() => import("../pages/Toggles"), "ToggleDetailPage");
const ToggleSettingsPage = lz(() => import("../pages/ToggleSettings"), "ToggleSettingsPage");
const SettingsVariablesPage = lz(() => import("../pages/Variables"), "SettingsVariablesPage");
const PlaygroundPage = lz(() => import("../pages/Playground"), "PlaygroundPage");
const RunsPage = lz(() => import("../pages/Activity"), "ActivityPage");
const RunPage = lz(() => import("../pages/Run"), "RunPage");
const ApprovalsPage = lz(() => import("../pages/Decisions"), "DecisionsPage");
const SettingsApprovalsPage = lz(() => import("../pages/SettingsApprovals"), "SettingsApprovalsPage");
const DeploymentsPage = lz(() => import("../pages/DeploymentsOverview"), "DeploymentsOverviewPage");
const EnvironmentsPage = lz(() => import("../pages/Environments"), "EnvironmentsPage");
const EnvironmentPage = lz(() => import("../pages/EnvironmentView"), "EnvironmentViewPage");
const ReleasesRoot = lz(() => import("../pages/ReleasesRoot"), "ReleasesRoot");
const ApplicationsPage = lz(() => import("../pages/ApplicationsList"), "ApplicationsListPage");
const ApplicationPage = lz(() => import("../pages/ApplicationView"), "ApplicationViewPage");
const LibraryPage = lz(() => import("../pages/Library"), "LibraryPage");
const StartersPage = lz(() => import("../pages/Starters"), "StartersPage");
const TemplatesPage = lz(() => import("../pages/Library"), "TemplatesPage");
const TemplatePage = lz(() => import("../pages/TemplatePage"), "TemplatePage");
const ImagesPage = lz(() => import("../pages/Images"), "ImagesPage");
const ComponentsPage = lz(() => import("../pages/Components"), "ComponentsPage");
const RegistryPage = lz(() => import("../pages/Registry"), "RegistryPage");
const ImageDetailPage = lz(() => import("../pages/ImageDetail"), "ImageDetailPage");
const PipelinesPage = lz(() => import("../pages/PipelinesList"), "PipelinesPage");
const LearnPage = lz(() => import("../learn/Learn"), "LearnRoot");
const KitPage = lz(() => import("../pages/Kit"), "KitPage");
const ComingPage = lz(() => import("../shell/Coming"), "ComingPage");
const SettingsPage = lz(() => import("../pages/SettingsHome"), "SettingsHomePage");
const AccessPage = lz(() => import("../pages/Settings"), "AccessPage");
const RunnersPage = lz(() => import("../pages/Runners"), "RunnersPage");
const TokensPage = lz(() => import("../pages/Tokens"), "TokensPage");
const RepositoryPage = lz(() => import("../pages/Repository"), "RepositoryPage");
const PullRequestsPage = lz(() => import("../pages/PullRequests"), "PullRequestsPage");
const NewPullRequestPage = lz(() => import("../pages/PullRequests"), "NewPullRequestPage");
const RepoCodeRoute = lz(() => import("../pages/repo/Route"), "RepoCodeRoute");
const PullRequestPage = lz(() => import("../pages/PullRequest"), "PullRequestPage");
const ImportRepositoriesPage = lz(() => import("../pages/ImportRepositories"), "ImportRepositoriesPage");
const DependenciesPage = lz(() => import("../pages/Dependencies"), "DependenciesPage");
const UpdatesPage = lz(() => import("../pages/Dependencies"), "UpdatesPage");
const RepositoryGraphPage = lz(() => import("../pages/SoftwareGraph"), "RepositoryGraphPage");
const GraphPage = lz(() => import("../pages/SoftwareGraph"), "GraphPage");
const AgentsPage = lz(() => import("../pages/Agents"), "AgentsPage");
const PolicyPage = lz(() => import("../pages/Policy"), "PolicyPage");
const SettingsSecurityPage = lz(() => import("../pages/SettingsSecurity"), "SettingsSecurityPage");
const SettingsWordsPage = lz(() => import("../pages/SettingsWords"), "SettingsWordsPage");
const AuditPage = lz(() => import("../pages/Audit"), "AuditPage");
const ConfigHomePage = lz(() => import("../pages/ConfigHome"), "ConfigHomePage");
const ConfigListPage = lz(() => import("../pages/ConfigList"), "ConfigListPage");
const ConfigObjectPage = lz(() => import("../pages/ConfigObject"), "ConfigObjectPage");
const SetupGuidePage = lz(() => import("../pages/SetupGuide"), "SetupGuidePage");
const WorkRoutes = lazy(() => import("../work/WorkRoutes"));
const UnitListPage = lz(() => import("../pages/squadrons/UnitList"), "UnitListPage");
const AddSquadronPage = lz(() => import("../pages/squadrons/AddSquadron"), "AddSquadronPage");
const SquadronViewPage = lz(() => import("../pages/squadrons/SquadronPage"), "SquadronViewPage");
const AddFlightPage = lz(() => import("../pages/squadrons/AddFlight"), "AddFlightPage");
const ChooseSquadronPage = lz(() => import("../pages/squadrons/ChooseSquadron"), "ChooseSquadronPage");
const OrganizePage = lz(() => import("../pages/OrganizeSquadrons"), "OrganizePage");
const WizardPage = lz(() => import("../pages/wizards/Wizards"), "WizardPage");

/** The page for an address. The key makes a page that shows one thing start afresh for another. */
export function PageFor({ path, approvals }: { path: string; approvals: Approvals }) {
  const at = locate(path);
  if (!at) return <NotFound />;
  const p = at.params;
  if (at.info.section === "work") return <WorkRoutes path={path} />;
  switch (at.info.pattern) {
    case "/": return <HomePage />;
    case "/projects": return <ProjectsPage />;
    case "/projects/new": return <ChooseSquadronPage />;
    case "/units": return <UnitListPage />;
    case "/units/new": return <AddSquadronPage />;
    case "/units/:name": return <SquadronViewPage key={p.name} name={p.name} />;
    case "/units/:name/projects/new": return <AddFlightPage key={p.name} unit={p.name} />;
    case "/projects/:name": return <ProjectPage key={p.name} name={p.name} />;
    case "/projects/:name/pipeline": return <PipelinePage key={p.name} name={p.name} />;
    case "/projects/:name/pipeline/edit": return <EditPipelinePage key={p.name} name={p.name} />;
    case "/projects/:name/import": return <ImportPipelinePage key={p.name} name={p.name} />;
    case "/projects/:name/tests": return <ProjectTestsPage key={p.name} name={p.name} />;
    case "/projects/:name/security": return <ProjectSecurityPage key={p.name} name={p.name} />;
    case "/projects/:name/standards": return <ProjectStandardsPage key={p.name} name={p.name} />;
    case "/projects/:name/caches": return <ProjectCachesPage key={p.name} name={p.name} />;
    case "/projects/:name/configuration": return <ProjectConfigurationPage key={p.name} name={p.name} />;
    case "/runs/:id/configuration/:step": return <ConfigReportPage key={p.id + p.step} run={p.id} step={p.step} />;
    case "/projects/:name/variables": return <ProjectVariablesPage key={p.name} name={p.name} />;
    case "/setup/guide": return <SetupGuidePage />;
    case "/projects/:name/toggles": return <ProjectTogglesPage key={p.name} name={p.name} />;
    case "/projects/:name/toggles/:toggle": return <ToggleDetailPage key={p.name + "/" + p.toggle} project={p.name} toggle={p.toggle} />;
    case "/projects/:name/toggle-settings": return <ToggleSettingsPage key={p.name} name={p.name} />;
    case "/repositories/import": return <ImportRepositoriesPage />;
    case "/repositories/:owner/:name": return <RepoCodeRoute key={p.owner + "/" + p.name + "@"} view="overview" path={p.owner + "/" + p.name} />;
    case "/repositories/:owner/:name/:view": case "/repositories/:owner/:name/commit/:sha":
      return <RepoCodeRoute key={p.owner + "/" + p.name + "@" + (p.sha ?? "")} view={p.view ?? "commit"} path={p.owner + "/" + p.name} sha={p.sha} />;
    case "/repositories/:owner/:name/pulls": return <PullRequestsPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} />;
    case "/repositories/:owner/:name/pulls/new": return <NewPullRequestPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} />;
    case "/repositories/:owner/:name/pulls/:number": return <PullRequestPage key={p.owner + "/" + p.name + "#" + p.number} path={p.owner + "/" + p.name} number={Number(p.number)} />;
    case "/repositories/:owner/:name/settings": return <RepositoryPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} tab="settings" />;
    case "/repositories/:owner/:name/mirrors": return <RepositoryPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} tab="mirrors" />;
    case "/repositories/:owner/:name/dependencies": return <DependenciesPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} />;
    case "/repositories/:owner/:name/graph": return <RepositoryGraphPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} />;
    case "/graph": return <GraphPage />;
    case "/repositories/:owner/:name/updates": return <UpdatesPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} />;
    case "/repositories/:owner/:name/history": return <RepositoryPage key={p.owner + "/" + p.name} path={p.owner + "/" + p.name} tab="history" />;
    case "/playground": return <PlaygroundPage />;
    case "/activity": return <RunsPage />;
    case "/activity/approvals": return <ApprovalsPage approvals={approvals} />;
    case "/activity/events": return <RunsPage events />;
    case "/runs/:id": return <RunPage id={p.id} />;
    case "/deployments": return <DeploymentsPage />;
    case "/deployments/environments": return <EnvironmentsPage />;
    case "/deployments/environments/:name": return <EnvironmentPage key={p.name} name={p.name} />;
    case "/deployments/releases": case "/deployments/releases/:a": case "/deployments/releases/:a/:b": case "/deployments/releases/:a/:b/get/:c": return <ReleasesRoot key={path} a={p.a} b={p.b} c={p.c} />;
    case "/deployments/applications": return <ApplicationsPage />;
    case "/deployments/applications/:name": return <ApplicationPage key={p.name} name={p.name} />;
    case "/library": return <LibraryPage />;
    case "/library/starters": return <StartersPage />;
    case "/library/templates": return <TemplatesPage />;
    case "/library/templates/:library/:name": return <TemplatePage key={p.library + "/" + p.name} library={p.library} name={p.name} />;
    case "/depot": return <ImagesPage />;
    case "/depot/components": return <ComponentsPage />;
    case "/depot/store": return <RegistryPage />;
    case "/depot/store/:project": return <RegistryPage key={p.project} project={p.project} />;
    case "/depot/digest/:digest": return <ImageDetailPage key={p.digest} digest={p.digest} />;
    case "/pipelines": return <PipelinesPage />;
    case "/kit": return <KitPage />;
    case "/learn": case "/learn/:a": case "/learn/:a/:b": return <LearnPage path={path} />;
    case "/code/:page": case "/depot/:page": case "/traffic": case "/traffic/:page": case "/changes": case "/changes/:page":
    case "/deployments/hosts": case "/deployments/machine-images":
      return <ComingPage path={path.length > 1 ? path.replace(/\/+$/, "") : path} />;
    case "/settings": return <SettingsPage />;
    case "/settings/runners": return <RunnersPage />;
    case "/settings/access": return <AccessPage />;
    case "/settings/access/keys": return <TokensPage />;
    case "/settings/access/ssh-keys": return <SshKeysPage />;
    case "/settings/access/agents": return <AgentsPage />;
    case "/repositories": return <RepositoriesPage />;
    case "/owners/:owner": return <OwnerPage key={p.owner} owner={p.owner} />;
    case "/settings/approvals": return <SettingsApprovalsPage />;
    case "/settings/rules": return <PolicyPage />;
    case "/settings/audit": return <AuditPage />;
    case "/settings/variables": return <SettingsVariablesPage />;
    case "/settings/words": return <SettingsWordsPage />;
    case "/settings/standards": return <SettingsStandardsPage />;
    case "/settings/security": return <SettingsSecurityPage />;
    case "/config": return <ConfigHomePage />;
    case "/units/organize": return <OrganizePage />;
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

