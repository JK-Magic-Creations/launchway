import { expect } from "vitest";

/** Names in the page that are not part of a link, a form control or a heading that is the thing's own title. */
const OK_ANCESTORS = "a, option, input, textarea, select, button, h1, [data-self], title, [data-allow-text]";

/** Fails if one of the known entity names is written in the page as plain text.
 * Every name must be a link to its page (docs/37 section g); this is the lint for it. */
export function plainEntityNames(root: HTMLElement, names: string[]): string[] {
  const found: string[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = n.textContent ?? "";
    const parent = (n as Text).parentElement;
    if (!parent || parent.closest(OK_ANCESTORS)) continue;
    for (const name of names) {
      const re = new RegExp(`(?<![\\w/-])${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?![\\w-]|\\.\\w|/)`);
      if (re.test(text)) found.push(`${name}: "${text.trim().slice(0, 60)}"`);
    }
  }
  return found;
}

export function expectEntitiesLinked(root: HTMLElement, names: string[]) {
  expect(plainEntityNames(root, names)).toEqual([]);
}
