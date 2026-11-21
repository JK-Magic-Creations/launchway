import { useMemo } from "react";

export interface Row { kind: "hunk" | "add" | "del" | "ctx"; old?: number; neu?: number; text: string }

/** parsePatch turns a unified patch of one file into rows. */
export function parsePatch(diff: string): Row[] {
  const rows: Row[] = [];
  let o = 0, n = 0, inHunk = false;
  for (const line of diff.split("\n")) {
    const h = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(line);
    if (h) { o = Number(h[1]); n = Number(h[2]); inHunk = true; rows.push({ kind: "hunk", text: line }); continue; }
    if (!inHunk) continue;
    if (line.startsWith("+")) rows.push({ kind: "add", neu: n++, text: line.slice(1) });
    else if (line.startsWith("-")) rows.push({ kind: "del", old: o++, text: line.slice(1) });
    else if (line.startsWith(" ")) rows.push({ kind: "ctx", old: o++, neu: n++, text: line.slice(1) });
  }
  return rows;
}

/** A read-only unified diff of one file, drawn with the same table as a pull request's. */
export function DiffTable({ path, diff }: { path: string; diff: string }) {
  const rows = useMemo(() => parsePatch(diff), [diff]);
  return (
    <table className="diff" aria-label={`Changes to ${path}`}>
      <caption className="sr-only">Changes to {path}</caption>
      <colgroup><col style={{ width: 48 }} /><col style={{ width: 48 }} /><col style={{ width: 20 }} /><col /></colgroup>
      {rows.map((r, i) => r.kind === "hunk"
        ? <tbody key={i}><tr className="hunk"><td colSpan={4}>{r.text}</td></tr></tbody>
        : <tbody key={i}><tr className={r.kind}>
          <td className="num">{r.old ?? ""}</td><td className="num">{r.neu ?? ""}</td>
          <td className="sign" aria-hidden="true">{r.kind === "add" ? "+" : r.kind === "del" ? "-" : ""}</td><td>{r.text}</td></tr></tbody>)}
    </table>
  );
}
