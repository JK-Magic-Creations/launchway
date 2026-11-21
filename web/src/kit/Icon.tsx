import { ICON_PATHS, type IconName } from "./iconPaths";

export type { IconName };

/** Icon draws one of the Launchway icons. It is decorative: the words next to it carry the meaning,
 * so it is hidden from screen readers. size: s is 15 px, l is 22 px, xl is 28 px, the default is 18 px. */
export function Icon({ name, size, className }: { name: IconName; size?: "s" | "l" | "xl"; className?: string }) {
  return (
    <svg className={["i", size, className].filter(Boolean).join(" ")} viewBox="0 0 24 24" aria-hidden="true"
      dangerouslySetInnerHTML={{ __html: ICON_PATHS[name] ?? "" }} />
  );
}

/** Whether a name is one of the icons. */
export function isIcon(name: string): name is IconName {
  return Object.prototype.hasOwnProperty.call(ICON_PATHS, name);
}
