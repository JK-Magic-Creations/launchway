import "./guide";
import { Link } from "../../components/ui";
import { EmptyState } from "../../kit/layout";
import { BlamePage } from "./BlamePage";
import { BranchesPage, TagsPage } from "./BranchesPage";
import { CodePage } from "./CodePage";
import { CommitPage } from "./CommitPage";
import { CommitsPage } from "./CommitsPage";
import { ComparePage } from "./ComparePage";
import { OverviewPage } from "./OverviewPage";
import { SearchPage } from "./SearchPage";
import { SecurityPage } from "./SecurityPage";
import { DivPage } from "../../kit/bits";

/** The one lazy entry to every page that reads a repository's code. */
export function RepoCodeRoute({ view, path, sha }: { view: string; path: string; sha?: string }) {
  switch (view) {
    case "security": return <SecurityPage path={path} />;
    case "overview": return <OverviewPage path={path} />;
    case "blame": return <BlamePage path={path} />;
    case "commits": return <CommitsPage path={path} />;
    case "commit": return <CommitPage path={path} sha={sha ?? ""} />;
    case "compare": return <ComparePage path={path} />;
    case "branches": return <BranchesPage path={path} />;
    case "tags": return <TagsPage path={path} />;
    case "search": return <SearchPage path={path} />;
    case "code": return <CodePage path={path} />;
    default: return <DivPage><EmptyState title="Nothing here">There is no page called {view} in a repository. <Link href={`/repositories/${path}/code`}>Open the code</Link>.</EmptyState></DivPage>;
  }
}
