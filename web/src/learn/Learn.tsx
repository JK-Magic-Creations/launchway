import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type MouseEvent } from "react";
import { api } from "../api";
import { Icon } from "../kit/Icon";
import { CrumbList } from "../nav/Navigation";
import { navigate, onLink } from "../router";
import { learnHref, loadArticle, loadIndex, loadManifest, type Article, type Manifest, type SearchIndex } from "./content";
import { search } from "./search";
import "../styles/learn.css";
import { terms } from "../terms";
import { PSub } from "../kit/bits";

const PLAYGROUND_KEY = "launchway.playground";

/** The version of the release, as the server reports it. */
function useVersion(): string {
  const [v, setV] = useState("");
  useEffect(() => { api.info().then((i) => setV(i.version)).catch(() => {}); }, []);
  return v;
}

function useLoaded<T>(load: () => Promise<T>, key: string): { value: T | null; error: string } {
  const [state, setState] = useState<{ key: string; value: T | null; error: string }>({ key, value: null, error: "" });
  useEffect(() => {
    let live = true;
    load().then((value) => live && setState({ key, value, error: "" }), (e) => live && setState({ key, value: null, error: e instanceof Error ? e.message : "The documentation did not load." }));
    return () => { live = false; };
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return state.key === key ? { value: state.value, error: state.error } : { value: null, error: "" };
}

/** The address's query string, kept current as the address changes (the router follows the path only). */
function useQuery(): string {
  const [q, setQ] = useState(() => new URLSearchParams(location.search).get("q") ?? "");
  useEffect(() => {
    const on = () => setQ(new URLSearchParams(location.search).get("q") ?? "");
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  return q;
}

/** Which Learn page an address is: the home, the search results, or a documentation page. */
export function learnRoute(path: string): { kind: "home" } | { kind: "search" } | { kind: "page"; slug: string } {
  const rest = path.replace(/^\/learn\/?/, "").replace(/\/+$/, "");
  if (rest === "") return { kind: "home" };
  if (rest === "search") return { kind: "search" };
  return { kind: "page", slug: rest };
}

/** The Learn area: a home with paths for different people, the documentation pages, and search. It needs no
 * sign-in: the pages are the same text as the public documentation, and show nothing about this installation. */
export function LearnRoot({ path }: { path: string }) {
  const route = learnRoute(path);
  const q = useQuery();
  const manifest = useLoaded(loadManifest, "m");
  const version = useVersion();
  const [navAt, setNavAt] = useState<string | null>(null); // the page the contents were opened on: they close on the next page
  const navOpen = navAt === path;
  const slug = route.kind === "page" ? route.slug : "";
  const section = manifest.value?.pages.find((p) => p.slug === slug)?.section;
  return (
    <div className="page learn">
      <div className="learn-top">
        <CrumbList crumbs={[{ label: terms().say("{Learn}"), href: route.kind === "home" ? undefined : "/learn" }, ...(route.kind === "search" ? [{ label: "Search" }] : [])]} />
        <SearchBox key={q} q={q} />
      </div>
      {manifest.error && <div className="errorbox" role="alert">{manifest.error}</div>}
      {!manifest.value && !manifest.error && <PSub>Loading…</PSub>}
      {manifest.value && (
        <div className={"learn-grid" + (route.kind === "home" ? " home" : "")}>
          {route.kind !== "home" && (
            <>
              <button type="button" className="btn btn-sm learn-navbtn" aria-expanded={navOpen} onClick={() => setNavAt(navOpen ? null : path)}><Icon name="menu" size="s" />Contents</button>
              <nav className={"learn-nav" + (navOpen ? " open" : "")} aria-label="Documentation">
                <Tree manifest={manifest.value} slug={slug} open={section} />
              </nav>
            </>
          )}
          {route.kind === "home" && <Home manifest={manifest.value} version={version} />}
          {route.kind === "search" && <Results q={q} />}
          {route.kind === "page" && <Page key={route.slug} slug={route.slug} manifest={manifest.value} version={version} />}
        </div>
      )}
    </div>
  );
}

function Tree({ manifest, slug, open }: { manifest: Manifest; slug: string; open?: string }) {
  return (
    <ul className="learn-tree">
      {manifest.sections.map((s) => (
        <li key={s.id}>
          <details open={s.id === open || (!open && s.id === "start")}>
            <summary>{s.title}</summary>
            <ul>
              {s.items.map((i) => (
                <li key={i.slug}><a href={learnHref(i.slug)} onClick={onLink} aria-current={i.slug === slug ? "page" : undefined}>{i.title}</a></li>
              ))}
            </ul>
          </details>
        </li>
      ))}
    </ul>
  );
}

function SearchBox({ q }: { q: string }) {
  const [text, setText] = useState(q);
  const go = (e: FormEvent) => { e.preventDefault(); if (text.trim()) navigate("/learn/search?q=" + encodeURIComponent(text.trim())); };
  return (
    <form className="learn-search" role="search" onSubmit={go}>
      <Icon name="search" size="s" />
      <input type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder="Search the documentation" aria-label="Search the documentation" />
    </form>
  );
}

function VersionNote({ version }: { version: string }) {
  if (!version) return null;
  return <p className="learn-version"><Icon name="tag" size="s" />These pages describe Launchway {version}, the release you are running.</p>;
}

function Home({ manifest, version }: { manifest: Manifest; version: string }) {
  const title = useMemo(() => new Map(manifest.pages.map((p) => [p.slug, p.title])), [manifest]);
  useEffect(() => { document.title = "Learn · Launchway"; }, []);
  return (
    <div className="learn-home">
      <header className="learn-hero">
        <div className="eyebrow">{terms().say("{Learn}")}</div>
        <h1>Learn Launchway</h1>
        <p className="lead">Everything you need to understand Launchway, in the product itself, and always for the release you run. No other website is needed.</p>
        <div className="learn-hero-actions">
          <a className="btn btn-primary" href={learnHref("start-here")} onClick={onLink}>Start here<Icon name="arrow" size="s" /></a>
          <a className="btn" href={learnHref("concepts")} onClick={onLink}>The concepts</a>
        </div>
        <VersionNote version={version} />
      </header>
      <h2 className="learn-h">Choose your path</h2>
      <div className="learn-paths">
        {manifest.paths.map((p) => (
          <section className="learn-path" key={p.id} aria-labelledby={"path-" + p.id}>
            <h3 id={"path-" + p.id}>{p.title}</h3>
            <p>{p.who}</p>
            <ol>
              {p.steps.map((s) => <li key={s}><a href={learnHref(s)} onClick={onLink}>{title.get(s) ?? s}</a></li>)}
            </ol>
          </section>
        ))}
      </div>
      <h2 className="learn-h">Everything</h2>
      <div className="learn-sections">
        {manifest.sections.map((s) => (
          <a className="learn-card" key={s.id} href={learnHref(s.items[0]?.slug ?? "")} onClick={onLink}>
            <b>{s.title}</b><span>{s.blurb}</span><small>{s.items.length} {s.items.length === 1 ? "page" : "pages"}</small>
          </a>
        ))}
      </div>
    </div>
  );
}

function Page({ slug, manifest, version }: { slug: string; manifest: Manifest; version: string }) {
  const { value: a, error } = useLoaded(() => loadArticle(slug), "p:" + slug);
  useEffect(() => { document.title = a ? `${a.title} · Learn · Launchway` : "Learn · Launchway"; }, [a]);
  if (error) return <div className="learn-article"><div className="errorbox" role="alert">{error} <a href="/learn" onClick={onLink}>Back to Learn</a></div></div>;
  if (!a) return <div className="learn-article"><PSub>Loading…</PSub></div>;
  return <ArticleView a={a} manifest={manifest} version={version} />;
}

function ArticleView({ a, manifest, version }: { a: Article; manifest: Manifest; version: string }) {
  const body = useRef<HTMLDivElement>(null);
  const section = manifest.sections.find((s) => s.id === a.section);
  // The code colours of this release, as classes the pages refer to.
  const style = useMemo(() => manifest.palette.map(([l, d], i) => `.learn-body .c${i}{--l:${l};--d:${d}}`).join(""), [manifest.palette]);

  // Pipelines marked as examples get a button that opens them in the playground.
  useEffect(() => {
    const el = body.current;
    if (!el) return;
    for (const box of el.querySelectorAll(".lx-try")) {
      if (box.querySelector(".lx-run")) continue;
      const b = document.createElement("button");
      b.type = "button"; b.className = "btn btn-sm lx-run"; b.textContent = "Try it in the editor";
      b.title = "Opens the playground with this pipeline. It checks the text as you type; nothing runs and nothing is saved.";
      box.appendChild(b);
    }
  }, [a]);
  // A link with a heading on it scrolls there once the page is shown.
  useEffect(() => {
    const id = decodeURIComponent(location.hash.slice(1));
    const t = id ? document.getElementById(id) : null;
    if (t) t.scrollIntoView(); else if (!id) window.scrollTo(0, 0);
  }, [a]);

  const click = useCallback((e: MouseEvent<HTMLDivElement>) => {
    const el = e.target as HTMLElement;
    const link = el.closest("a");
    if (link) {
      const href = link.getAttribute("href") ?? "";
      if (href.startsWith("#")) {
        e.preventDefault();
        history.replaceState(null, "", location.pathname + location.search + href);
        document.getElementById(decodeURIComponent(href.slice(1)))?.scrollIntoView();
        return;
      }
      if (href.startsWith("/")) {
        e.preventDefault();
        if (e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) { window.open(href, "_blank"); return; }
        const [p, hash] = href.split("#");
        navigate(p);
        if (hash && p === location.pathname) document.getElementById(hash)?.scrollIntoView();
        else if (hash) history.replaceState(null, "", p + "#" + hash);
        return;
      }
    }
    const copy = el.closest("button.copy");
    if (copy) {
      const code = copy.closest("div[class*='language-']")?.querySelector("code")?.textContent ?? "";
      void navigator.clipboard?.writeText(code).then(() => { copy.classList.add("copied"); setTimeout(() => copy.classList.remove("copied"), 1500); }, () => {});
      return;
    }
    if (el.closest(".lx-run")) {
      const code = el.closest(".lx-try")?.querySelector("code")?.textContent ?? "";
      try { localStorage.setItem(PLAYGROUND_KEY, code.endsWith("\n") ? code : code + "\n"); } catch { /* the playground starts with its own example */ }
      navigate("/playground");
    }
  }, []);

  return (
    <>
      <article className="learn-article">
        <style>{style}</style>
        <div className="eyebrow">{section?.title ?? terms().say("{Learn}")}</div>
        <h1>{a.title}</h1>
        {/* The text is built from docs/ by the release, not typed by anyone who uses this server. */}
        {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions */}
        <div className="learn-body" ref={body} onClick={click} dangerouslySetInnerHTML={{ __html: a.html }} />
        <nav className="learn-pn" aria-label="Previous and next page">
          {a.prev ? <a className="prev" href={learnHref(a.prev.slug)} onClick={onLink}><small>Previous</small>{a.prev.title}</a> : <span />}
          {a.next ? <a className="next" href={learnHref(a.next.slug)} onClick={onLink}><small>Next</small>{a.next.title}</a> : <span />}
        </nav>
        <VersionNote version={version} />
      </article>
      {a.toc.length > 0 && (
        <aside className="learn-toc" aria-label="On this page">
          <div className="eyebrow">On this page</div>
          <ul>
            {a.toc.map((h) => (
              <li key={h.id} className={"l" + h.level}>
                <a href={"#" + h.id} onClick={(e) => { e.preventDefault(); history.replaceState(null, "", location.pathname + "#" + h.id); document.getElementById(h.id)?.scrollIntoView(); }}>{h.text}</a>
              </li>
            ))}
          </ul>
        </aside>
      )}
    </>
  );
}

function Results({ q }: { q: string }) {
  const { value: idx, error } = useLoaded<SearchIndex>(loadIndex, "i");
  const manifest = useLoaded(loadManifest, "m").value;
  useEffect(() => { document.title = `Search: ${q} · Learn · Launchway`; }, [q]);
  const hits = useMemo(() => (idx ? search(idx, q) : []), [idx, q]);
  const title = useMemo(() => new Map((manifest?.pages ?? []).map((p) => [p.slug, p.title])), [manifest]);
  return (
    <section className="learn-article learn-results" aria-live="polite">
      <div className="eyebrow">Search</div>
      <h1>{q ? <>Results for “{q}”</> : "Search the documentation"}</h1>
      {error && <div className="errorbox" role="alert">{error}</div>}
      {!idx && !error && <PSub>Searching…</PSub>}
      {idx && q && hits.length === 0 && <PSub>Nothing found for “{q}”. Try fewer or simpler words, or <a href="/learn" onClick={onLink}>browse the contents</a>.</PSub>}
      {idx && !q && <PSub>Type a word or two in the box above.</PSub>}
      <ol className="learn-hits">
        {hits.map((h) => (
          <li key={h.slug + "#" + h.anchor}>
            <a href={learnHref(h.slug, h.anchor)} onClick={onLink}>
              <span className="t">{title.get(h.slug) ?? h.slug}{h.heading ? <span className="h"> › {h.heading}</span> : null}</span>
              <span className="s">{h.snippet}…</span>
            </a>
          </li>
        ))}
      </ol>
    </section>
  );
}
