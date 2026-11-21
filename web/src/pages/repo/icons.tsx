// The small pictures of the code pages: a folder or a kind of file in the tree, and the buttons of a file's header.
// Inline SVG, drawn with the text color, never an icon font; they live in the code pages' lazy chunk, so the first
// load does not carry them. Every picture is decoration: the name of the thing, or the button's label, says what it is.
import type { ReactNode } from "react";

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
  if (lower === "dockerfile" || lower === "makefile" || lower === "jenkinsfile") return "config";
  if (lower === "license" || lower === "licence" || lower === "readme" || lower === "authors" || lower === "notice") return "doc";
  if (lower === "package-lock.json" || lower === "go.sum") return "lock";
  if (lower.startsWith(".") && !lower.includes(".", 1)) return "config";
  const dot = lower.lastIndexOf(".");
  return (dot > 0 ? BY_EXT[lower.slice(dot + 1)] : undefined) ?? "file";
}

const KIND_WORD: Record<FileKind, string> = { folder: "folder", code: "source file", doc: "document", image: "picture", config: "settings file", shell: "script", archive: "archive", lock: "lock file", link: "link", module: "submodule", file: "file" };
export const kindWord = (k: FileKind) => KIND_WORD[k];

const PAGE = <path d="M4 1.7h5.2L12.5 5v9.3H4z M9 1.7V5h3.5" />;

/** The picture for a folder or a kind of file. */
export function KindIcon({ kind }: { kind: FileKind }) {
  switch (kind) {
    case "folder": return <Svg><path d="M1.7 4.2c0-.6.4-1 1-1h3.2l1.5 1.7h6c.6 0 1 .4 1 1v6.4c0 .6-.4 1-1 1H2.7c-.6 0-1-.4-1-1z" /></Svg>;
    case "code": return <Svg>{PAGE}<path d="M7 8.2 5.8 9.4 7 10.6 M9.6 8.2l1.2 1.2-1.2 1.2" /></Svg>;
    case "doc": return <Svg>{PAGE}<path d="M6 8h4 M6 10h4 M6 12h2.5" /></Svg>;
    case "image": return <Svg>{PAGE}<circle cx="7" cy="8.4" r=".9" /><path d="m5 13 2.6-2.4 1.6 1.4 1.4-1.2 1.4 1.4" /></Svg>;
    case "config": return <Svg>{PAGE}<path d="M6 8.2h4 M6 10.4h4" /><circle cx="7.4" cy="8.2" r=".4" /><circle cx="8.8" cy="10.4" r=".4" /></Svg>;
    case "shell": return <Svg>{PAGE}<path d="m6 8.2 1.4 1.2L6 10.6 M8.6 11h2" /></Svg>;
    case "archive": return <Svg>{PAGE}<path d="M8 3v1.2 M8 5.4v1.2 M8 7.8v1.2 M7.2 10h1.6v2H7.2z" /></Svg>;
    case "lock": return <Svg>{PAGE}<rect x="6" y="9" width="4" height="3" rx=".6" /><path d="M6.8 9V8a1.2 1.2 0 0 1 2.4 0v1" /></Svg>;
    case "link": return <Svg><path d="M6.6 9.4 9.4 6.6 M7.4 4.6l.9-.9a2.4 2.4 0 0 1 3.4 3.4l-.9.9 M8.6 11.4l-.9.9a2.4 2.4 0 0 1-3.4-3.4l.9-.9" /></Svg>;
    case "module": return <Svg><path d="M2.4 4.2 8 1.8l5.6 2.4v7.6L8 14.2l-5.6-2.4z M2.6 4.3 8 6.8l5.4-2.5 M8 6.8v7.2" /></Svg>;
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
    case "copy": return <Svg><rect x="5.4" y="5.4" width="8" height="8" rx="1.2" /><path d="M10.6 5.4V3.6c0-.7-.5-1.2-1.2-1.2H3.6c-.7 0-1.2.5-1.2 1.2v5.8c0 .7.5 1.2 1.2 1.2h1.8" /></Svg>;
    case "link": return <Svg><path d="M6.6 9.4 9.4 6.6 M7.4 4.6l.9-.9a2.4 2.4 0 0 1 3.4 3.4l-.9.9 M8.6 11.4l-.9.9a2.4 2.4 0 0 1-3.4-3.4l.9-.9" /></Svg>;
    case "raw": return <Svg>{PAGE}<path d="M8.2 7.6v3.8 M6.6 9.9l1.6 1.6 1.6-1.6" /></Svg>;
    case "blame": return <Svg><circle cx="8" cy="5.6" r="2.3" /><path d="M3.4 13.6c.4-2.4 2.2-3.7 4.6-3.7s4.2 1.3 4.6 3.7" /></Svg>;
    case "history": return <Svg><path d="M2.6 8a5.4 5.4 0 1 0 1.7-3.9 M2.4 2.6v2.3h2.3 M8 5v3.2l2 1.2" /></Svg>;
    case "edit": return <Svg><path d="m10.6 2.8 2.6 2.6-7.6 7.6-3 .4.4-3z M9.4 4l2.6 2.6" /></Svg>;
    case "delete": return <Svg><path d="M2.8 4.2h10.4 M6 4.2V2.8h4v1.4 M4 4.2l.6 9h6.8l.6-9 M6.8 6.8v4 M9.2 6.8v4" /></Svg>;
    case "search": return <Svg><circle cx="7" cy="7" r="4" /><path d="m10 10 3.4 3.4" /></Svg>;
    case "compare": return <Svg><circle cx="4.4" cy="3.8" r="1.4" /><circle cx="4.4" cy="12.2" r="1.4" /><circle cx="11.6" cy="12.2" r="1.4" /><path d="M4.4 5.2v5.6 M11.6 10.8V6.4a2 2 0 0 0-2-2H8 M9.2 2.8 7.8 4.4l1.4 1.6" /></Svg>;
  }
}
