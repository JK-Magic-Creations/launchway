// A person's own settings for how the interface behaves, kept in the browser. Where storage is blocked they
// fall back to the defaults.

const SHORTCUT_KEY = "lw-shortcut";

/** Whether the slash key opens the search. On by default; a person who uses speech input or a screen reader's
 * single-key commands can turn it off (WCAG 2.1.4, character key shortcuts). */
export function shortcutOn(): boolean {
  try { return localStorage.getItem(SHORTCUT_KEY) !== "off"; } catch { return true; }
}

export function setShortcut(on: boolean): void {
  try { localStorage.setItem(SHORTCUT_KEY, on ? "on" : "off"); } catch { /* storage may be blocked */ }
}
