import { useEffect, useRef, type ReactNode } from "react";

/** Dialog is a modal layer: it takes focus, keeps Tab inside, closes on Escape or a click outside, and
 * gives focus back to what had it. */
export function Dialog({ label, onClose, children, className }: { label: string; onClose: () => void; children: ReactNode; className?: string }) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    const first = box.current?.querySelector<HTMLElement>("[data-autofocus], input, button, a[href]");
    first?.focus();
    return () => { before?.focus?.(); };
  }, []);
  const key = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") { e.stopPropagation(); onClose(); return; }
    if (e.key !== "Tab" || !box.current) return;
    const f = [...box.current.querySelectorAll<HTMLElement>("a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex='-1'])")];
    if (f.length === 0) return;
    const at = f.indexOf(document.activeElement as HTMLElement);
    if (e.shiftKey && at <= 0) { e.preventDefault(); f[f.length - 1].focus(); }
    else if (!e.shiftKey && at === f.length - 1) { e.preventDefault(); f[0].focus(); }
  };
  return (
    <div className="overlay" role="presentation" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }} onKeyDown={key}>
      <div className={"modal" + (className ? " " + className : "")} role="dialog" aria-modal="true" aria-label={label} ref={box}>{children}</div>
    </div>
  );
}
