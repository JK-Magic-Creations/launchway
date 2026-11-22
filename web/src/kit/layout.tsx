import { useId, type ReactNode } from "react";
import { ErrorBoundary } from "../components/ErrorBoundary";
import { onLink } from "../router";
import { Icon, type IconName } from "./Icon";

/** PageHead opens a page: an eyebrow, the title, one plain sentence about how things are, and the actions. */
export function PageHead({ title, sentence, eyebrow, actions, badge }: { title: ReactNode; sentence?: ReactNode; eyebrow?: ReactNode; actions?: ReactNode; badge?: ReactNode }) {
  return (
    <header className="phead">
      <div className="t">
        {eyebrow && <div className="eyebrow">{eyebrow}</div>}
        <h1>{title}{badge}</h1>
        {sentence && <p className="sentence">{sentence}</p>}
      </div>
      {actions && <div className="acts">{actions}</div>}
    </header>
  );
}

type ButtonKind = "pri" | "bad" | "ok" | "ghost";
const KIND: Record<ButtonKind, string> = { pri: "btn-primary", bad: "btn-danger-outline", ok: "btn-ok", ghost: "btn-ghost" };
const cls = (kind?: ButtonKind, sm?: boolean) => ["btn", kind && KIND[kind], sm && "btn-sm"].filter(Boolean).join(" ");

/** Button is the design's button: primary, risky, approve or quiet; with an icon on the left. */
export function Button({ kind, sm, icon, children, ...rest }: { kind?: ButtonKind; sm?: boolean; icon?: IconName; children: ReactNode } & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button type="button" {...rest} className={cls(kind, sm) + (rest.className ? " " + rest.className : "")}>{icon && <Icon name={icon} />}{children}</button>;
}

/** LinkButton is a link drawn as a button. It moves within the page without a reload. */
export function LinkButton({ href, kind, sm, icon, children }: { href: string; kind?: ButtonKind; sm?: boolean; icon?: IconName; children: ReactNode }) {
  return <a href={href} onClick={onLink} className={cls(kind, sm)}>{icon && <Icon name={icon} />}{children}</a>;
}

/** Sheet is a flat card with an optional header (title, and actions at the right). A fault inside it shows in the card and leaves the page. */
export function Sheet({ title, actions, children, pad, className, id }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; pad?: boolean; className?: string; id?: string }) {
  return (
    <section className={["sheet", pad && "pad", className].filter(Boolean).join(" ")} id={id}>
      {title && <div className="sh-h"><h2>{title}</h2>{actions && <div className="r">{actions}</div>}</div>}
      <ErrorBoundary name={typeof title === "string" ? `The ${title.toLowerCase()} card` : "This card"}>{children}</ErrorBoundary>
    </section>
  );
}

export interface BandCell { key: string; label: ReactNode; value: ReactNode; foot?: ReactNode; href?: string; loud?: "bad" | "warn" }

/** Band is the overview: three to five numbers that are also links or filters. A cell is loud only when it needs a person. */
export function Band({ cells }: { cells: BandCell[] }) {
  return (
    <div className="band">
      {cells.map((c) => {
        const body = (
          <>
            <div className="k">{c.label}</div>
            <div className="v num">{c.value}</div>
            {c.foot && <div className="f">{c.foot}</div>}
          </>
        );
        const loud = c.loud === "bad" ? "loud" : c.loud === "warn" ? "loud-w" : "";
        return c.href
          ? <a key={c.key} href={c.href} onClick={onLink} className={loud}>{body}</a>
          : <div key={c.key} className={loud}>{body}</div>;
      })}
    </div>
  );
}

/** EmptyState draws the line dotted, says in one sentence what this place is and what will appear, and offers one action. */
export function EmptyState({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty-v2">
      <svg className="art" viewBox="0 0 96 56" fill="none" aria-hidden="true">
        <path d="M6 40h84" stroke="currentColor" strokeWidth="3" strokeDasharray="2 7" strokeLinecap="round" />
        <circle cx="14" cy="40" r="6" stroke="currentColor" strokeWidth="3" />
        <circle cx="48" cy="40" r="6" stroke="currentColor" strokeWidth="3" strokeDasharray="3 4" />
        <circle cx="82" cy="40" r="6" stroke="currentColor" strokeWidth="3" strokeDasharray="3 4" />
      </svg>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
      {action && <div className="acts">{action}</div>}
    </div>
  );
}

/** DetailPanel is docked beside a drawing and shows the selected thing, so a quick look never needs a new page. */
export function DetailPanel({ title, status, children, actions, id }: { title: ReactNode; status?: ReactNode; children: ReactNode; actions?: ReactNode; id?: string }) {
  const heading = useId();
  return (
    <aside className="panel sheet" id={id} aria-labelledby={heading}>
      <div className="ph"><div>{status}</div><div style={{ flex: 1, minWidth: 0 }}><h3 id={heading}>{title}</h3></div>{actions}</div>
      {children}
    </aside>
  );
}

/** EvidenceList is the row of tiles under a decision: a number, a label and a plain line each. */
export function EvidenceList({ items }: { items: { key: string; label: string; value: ReactNode; note?: ReactNode }[] }) {
  return (
    <dl className="evid">
      {items.map((e) => (
        <div key={e.key}>
          <dt className="eyebrow">{e.label}</dt>
          <dd className="big">{e.value}</dd>
          {e.note && <dd className="evnote">{e.note}</dd>}
        </div>
      ))}
    </dl>
  );
}

/** Decision is the card for something a person decides: what, the evidence, and one recommended action. */
export function Decision({ icon = "gate", tone, title, children, evidence, actions }: {
  icon?: IconName; tone?: "warn" | "bad"; title: ReactNode; children?: ReactNode; evidence?: Parameters<typeof EvidenceList>[0]["items"]; actions?: ReactNode;
}) {
  return (
    <article className={`dcard ${tone ?? "warn"}`}>
      <div className="d-top">
        <div className={`ico-tile ${tone ?? "warn"}`}><Icon name={icon} /></div>
        <div className="tx"><h2>{title}</h2>{children && <p>{children}</p>}</div>
      </div>
      {evidence && evidence.length > 0 && <EvidenceList items={evidence} />}
      {actions && <div className="acts decide">{actions}</div>}
    </article>
  );
}
