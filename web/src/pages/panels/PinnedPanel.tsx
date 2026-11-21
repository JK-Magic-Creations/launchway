import { onLink } from "../../router";
import type { PanelProps } from "./registry";

/** A saved view: the address of a list with its filters, in words, and a way back to it. Only addresses on this server open. */
export default function PinnedPanel({ title, settings }: PanelProps<{ href?: string }>) {
  const href = typeof settings.href === "string" && settings.href.startsWith("/") && !settings.href.startsWith("//") ? settings.href : "";
  const [path, query] = href.split("?");
  const q = new URLSearchParams(query ?? "");
  const words = [...q.entries()].filter(([k]) => k !== "node").map(([k, v]) => `${k.replace(/_/g, " ")} ${v}`);
  return (
    <section className="pn" aria-label={title}>
      <h2>{title}</h2>
      <div className="pn-b">
        {href ? <>
          <p><a href={href} onClick={onLink} className="btn btn-sm btn-primary">Open this view</a></p>
          <p className="faint small">{path}{words.length > 0 ? ` with ${words.join(", ")}` : ""}</p>
        </> : <p className="pn-err">This saved view has no address on this server.</p>}
      </div>
    </section>
  );
}
