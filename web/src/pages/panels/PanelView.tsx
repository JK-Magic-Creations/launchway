import { lazy, Suspense, type ComponentType, type LazyExoticComponent } from "react";
import type { HomeAnswer } from "../../organizeApi";
import { PANEL_TYPES, type PanelDef, type PanelProps } from "./registry";
import "./panels.css";

// One lazy component for each type, made once at load, so a panel keeps its state when Home redraws.
const BODIES: Record<string, LazyExoticComponent<ComponentType<PanelProps>>> = Object.fromEntries(PANEL_TYPES.map((t) => [t.type, lazy(t.load)]));

/** Draws one panel of Home: loads the chunk of its type, and says plainly when the type is not known (a newer or removed one). */
export default function PanelView({ def, home, now }: { def: PanelDef; home: HomeAnswer; now: number }) {
  const Body = BODIES[def.type];
  if (!Body) return <section className="pn" aria-label={def.title}><h2>{def.title}</h2><div className="pn-b faint">This kind of panel is not available in this version.</div></section>;
  const props: PanelProps = { id: def.id, title: def.title, settings: def.settings, size: def.size, home, now };
  return <Suspense fallback={<section className="pn" aria-busy="true" aria-label={def.title}><h2>{def.title}</h2><div className="pn-b"><div className="pn-skel" /></div></section>}><Body {...props} /></Suspense>;
}
