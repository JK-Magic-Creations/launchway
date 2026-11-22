import { lazy, Suspense } from "react";
import { match } from "../router";

// The one entry the rest of the interface knows about: it picks the work page for an address and loads it. Each page is its own chunk.

const lz = <P,>(load: () => Promise<{ default: React.ComponentType<P> }>) => lazy(load);
const Home = lz(() => import("./WorkHome"));
const Inbox = lz(() => import("./Inbox"));
const Agents = lz(() => import("./Agents"));
const Item = lz<{ itemRef: string }>(() => import("./ItemPage"));
const Items = lz<{ scope: string }>(() => import("./ItemList"));
const Board = lz<{ scope: string }>(() => import("./Board"));
const Backlog = lz<{ scope: string }>(() => import("./Backlog"));
const Sprints = lz<{ scope: string }>(() => import("./Sprints"));
const Sprint = lz<{ scope: string; id: string }>(() => import("./Sprints").then((m) => ({ default: m.SprintPage })));
const Charts = lz<{ scope: string }>(() => import("./Charts"));
const Roadmap = lz<{ scope: string }>(() => import("./Plan").then((m) => ({ default: m.Roadmap })));
const Milestones = lz<{ scope: string }>(() => import("./Plan").then((m) => ({ default: m.Milestones })));
const Proposals = lz<{ scope: string }>(() => import("./Proposals"));
const Settings = lz<{ scope: string }>(() => import("./WorkSettings"));

/** The scope pages, by the last part of the address. */
const AREA: Record<string, React.ComponentType<{ scope: string }>> = { items: Items, board: Board, backlog: Backlog, sprints: Sprints, charts: Charts, roadmap: Roadmap, milestones: Milestones, proposals: Proposals, settings: Settings };

export default function WorkRoutes({ path }: { path: string }) {
  return <Suspense fallback={<div className="page"><div className="loading" role="status">Loading…</div></div>}>{pick(path)}</Suspense>;
}

function pick(path: string) {
  const clean = path.length > 1 ? path.replace(/\/+$/, "") : path;
  if (clean === "/work") return <Home />;
  if (clean === "/work/inbox") return <Inbox />;
  if (clean === "/work/agents") return <Agents />;
  const item = match("/work/item/:ref", clean);
  if (item) return <Item key={item.ref} itemRef={item.ref} />;
  const sprint = match("/work/:scope/sprints/:id", clean);
  if (sprint) return <Sprint key={sprint.scope + sprint.id} scope={sprint.scope} id={sprint.id} />;
  const area = match("/work/:scope/:area", clean);
  const Page = area ? AREA[area.area] : undefined;
  if (area && Page) return <Page key={area.scope + area.area} scope={area.scope} />;
  return <div className="page"><div className="empty"><div className="empty-title">Nothing here</div><div className="empty-body">This work page does not exist.</div></div></div>;
}
