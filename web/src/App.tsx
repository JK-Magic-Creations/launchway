import { useEffect, useState } from "react";
import { api } from "./api";
import { match, onLink, usePath } from "./router";
import { RunsPage } from "./pages/Runs";
import { RunPage } from "./pages/Run";
import { ProjectsPage } from "./pages/Projects";
import { RunnersPage } from "./pages/Runners";
import { PlaygroundPage } from "./pages/Playground";
import { Empty, Link } from "./components/ui";

const NAV = [
  { href: "/", label: "Runs", active: (p: string) => p === "/" || p.startsWith("/runs") },
  { href: "/projects", label: "Projects", active: (p: string) => p.startsWith("/projects") },
  { href: "/runners", label: "Runners", active: (p: string) => p.startsWith("/runners") },
  { href: "/playground", label: "Playground", active: (p: string) => p.startsWith("/playground") },
];

// Shown so people can see where this is going; none of these work yet.
const NEXT = ["Environments", "Releases", "Approvals", "Infrastructure", "Policy"];

export function App() {
  const path = usePath();
  const [version, setVersion] = useState("");
  useEffect(() => { api.info().then((i) => setVersion(i.version)).catch(() => {}); }, []);

  let page;
  let m;
  if (path === "/") page = <RunsPage />;
  else if ((m = match("/runs/:id", path))) page = <RunPage id={m.id} />;
  else if (path === "/projects") page = <ProjectsPage />;
  else if (path === "/runners") page = <RunnersPage />;
  else if (path === "/playground") page = <PlaygroundPage />;
  else page = <div className="page"><Empty title="Nothing here">This page does not exist. <Link href="/">Go to runs</Link>.</Empty></div>;

  useEffect(() => {
    const title = NAV.find((n) => n.active(path))?.label;
    document.title = title ? `${title} · Launchway` : "Launchway";
  }, [path]);

  return (
    <div className="shell">
      <nav className="nav" aria-label="Main">
        <a href="/" onClick={onLink} className="brand">
          <svg width="26" height="26" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#2563eb" /><path d="M8 22l8-12 8 12" stroke="#fff" strokeWidth="3" fill="none" strokeLinecap="round" strokeLinejoin="round" /></svg>
          <span>Launchway</span>
        </a>
        <div className="nav-links">
          {NAV.map((n) => (
            <a key={n.href} href={n.href} onClick={onLink} className={"nav-link" + (n.active(path) ? " nav-on" : "")}
              aria-current={n.active(path) ? "page" : undefined}>{n.label}</a>
          ))}
        </div>
        <div className="nav-next">
          <div className="nav-next-title">Coming next</div>
          {NEXT.map((n) => <div key={n} className="nav-soon" title="Designed, not built yet">{n}</div>)}
        </div>
        <div className="nav-foot">{version && <span>Launchway {version}</span>}</div>
      </nav>
      <main className="main">{page}</main>
    </div>
  );
}
