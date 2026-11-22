// The small pictures of the code pages: a folder or a kind of file in the tree, and the buttons of a file's header.
// Inline SVG, drawn with the text color, never an icon font; they live in the code pages' lazy chunk, so the first
// load does not carry them. Every picture is decoration: the name of the thing, or the button's label, says what it is.
import type { ReactNode } from "react";
import { prose } from "../../prose";
const P = await prose("pages.repo.icons");

const BODY = "currentColor";

function Svg({ children, size = 16 }: { children: ReactNode; size?: number }) {
  return (
    <svg className="rico" width={size} height={size} viewBox="0 0 16 16" fill="none" stroke={BODY} strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
      {children}
    </svg>
  );
}

export type FileKind = "folder" | "code" | "doc" | "image" | "config" | "shell" | "archive" | "lock" | "link" | "module" | "file";

const BY_EXT: Record<string, FileKind> = {
  go: "code", ts: "code", tsx: "code", js: "code", jsx: "code", mjs: "code", cjs: "code", py: "code", rb: "code", rs: "code", java: "code", kt: "code", c: "code", h: "code", cc: "code", cpp: "code",
  cs: "code", php: "code", swift: "code", css: "code", scss: "code", html: "code", htm: "code", sql: "code", lua: "code", tf: "code",
  md: "doc", markdown: "doc", txt: "doc", rst: "doc", adoc: "doc", pdf: "doc",
  png: "image", jpg: "image", jpeg: "image", gif: "image", webp: "image", avif: "image", svg: "image", ico: "image",
  yaml: "config", yml: "config", json: "config", toml: "config", ini: "config", cfg: "config", conf: "config", xml: "config", env: "config", mod: "config", sum: "lock", lock: "lock",
  sh: "shell", bash: "shell", zsh: "shell", fish: "shell", ps1: "shell",
  zip: "archive", gz: "archive", tgz: "archive", tar: "archive", xz: "archive", bz2: "archive", jar: "archive", wasm: "archive",
};

/** What kind of file a name is, from its ending (and a few well-known names). */
export function kindOf(name: string, type: string): FileKind {
  if (type === "dir") return "folder";
  if (type === "symlink") return "link";
  if (type === "submodule") return "module";
  const lower = name.toLowerCase();
  if (lower === "dockerfile" || lower === "makefile") return "config";
  if (lower === "license" || lower === "licence" || lower === "readme" || lower === "authors" || lower === "notice") return "doc";
  if (lower === "package-lock.json" || lower === "go.sum") return "lock";
  if (lower.startsWith(".") && !lower.includes(".", 1)) return "config";
  const dot = lower.lastIndexOf(".");
  return (dot > 0 ? BY_EXT[lower.slice(dot + 1)] : undefined) ?? "file";
}

const KIND_WORD: Record<FileKind, string> = { folder: "folder", code: "source file", doc: "document", image: "picture", config: "settings file", shell: "script", archive: "archive", lock: "lock file", link: "link", module: "submodule", file: "file" };
export const kindWord = (k: FileKind) => KIND_WORD[k];

const PAGE = <path d={P.t0} />;

/** The picture for a folder or a kind of file. */
export function KindIcon({ kind }: { kind: FileKind }) {
  switch (kind) {
    case "folder": return <Svg><path d={P.t1} /></Svg>;
    case "code": return <Svg>{PAGE}<path d={P.t2} /></Svg>;
    case "doc": return <Svg>{PAGE}<path d="M6 8h4 M6 10h4 M6 12h2.5" /></Svg>;
    case "image": return <Svg>{PAGE}<circle cx="7" cy="8.4" r=".9" /><path d={P.t3} /></Svg>;
    case "config": return <Svg>{PAGE}<path d="M6 8.2h4 M6 10.4h4" /><circle cx="7.4" cy="8.2" r=".4" /><circle cx="8.8" cy="10.4" r=".4" /></Svg>;
    case "shell": return <Svg>{PAGE}<path d={P.t4} /></Svg>;
    case "archive": return <Svg>{PAGE}<path d={P.t5} /></Svg>;
    case "lock": return <Svg>{PAGE}<rect x="6" y="9" width="4" height="3" rx=".6" /><path d={P.t6} /></Svg>;
    case "link": return <Svg><path d={P.t7} /></Svg>;
    case "module": return <Svg><path d={P.t8} /></Svg>;
    default: return <Svg>{PAGE}</Svg>;
  }
}

/** A file or folder name with its picture. */
export function NameIcon({ name, type }: { name: string; type: string }) {
  const k = kindOf(name, type);
  return <span className={"kind kind-" + k} title={kindWord(k)}><KindIcon kind={k} /></span>;
}

export type ActionIcon = "copy" | "link" | "raw" | "blame" | "history" | "edit" | "delete" | "search" | "compare";

/** The pictures of the buttons in a file's header. */
export function ActionPicture({ name }: { name: ActionIcon }) {
  switch (name) {
    case "copy": return <Svg><rect x="5.4" y="5.4" width="8" height="8" rx="1.2" /><path d={P.t9} /></Svg>;
    case "link": return <Svg><path d={P.t7} /></Svg>;
    case "raw": return <Svg>{PAGE}<path d={P.t10} /></Svg>;
    case "blame": return <Svg><circle cx="8" cy="5.6" r="2.3" /><path d={P.t11} /></Svg>;
    case "history": return <Svg><path d={P.t12} /></Svg>;
    case "edit": return <Svg><path d={P.t13} /></Svg>;
    case "delete": return <Svg><path d={P.t14} /></Svg>;
    case "search": return <Svg><circle cx="7" cy="7" r="4" /><path d="m10 10 3.4 3.4" /></Svg>;
    case "compare": return <Svg><circle cx="4.4" cy="3.8" r="1.4" /><circle cx="4.4" cy="12.2" r="1.4" /><circle cx="11.6" cy="12.2" r="1.4" /><path d={P.t15} /></Svg>;
  }
}
