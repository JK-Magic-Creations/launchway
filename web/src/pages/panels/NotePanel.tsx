import { Markdown } from "../../markdown";
import type { PanelProps } from "./registry";

/** Notes and links: Markdown through the safe renderer (no raw HTML, only http, https and local links). */
export default function NotePanel({ title, settings }: PanelProps<{ text?: string }>) {
  const text = typeof settings.text === "string" ? settings.text : "";
  return (
    <section className="pn" aria-label={title}>
      <h2>{title}</h2>
      <div className="pn-b">{text ? <Markdown text={text} /> : <p className="faint">This note is empty.</p>}</div>
    </section>
  );
}
