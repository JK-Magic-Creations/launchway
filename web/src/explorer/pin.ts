import { organizeApi } from "../organizeApi";

/** Pins a view (an address on this server) to the person's own Home as a "saved view" panel. */
export async function pinToHome(title: string, href: string): Promise<void> {
  const lay = await organizeApi.layout("home");
  const mine = lay.personal as { panels?: unknown[]; order?: string[] };
  const id = "p:" + Math.random().toString(36).slice(2, 9);
  const panels = [...(Array.isArray(mine.panels) ? mine.panels : []), { id, type: "pinned", title: title.slice(0, 80), size: 1, settings: { href } }];
  await organizeApi.setMyLayout("home", { ...mine, panels, order: [...(mine.order ?? []), id] });
}
