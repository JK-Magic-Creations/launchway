// Reads one JSON file the build ships beside the scripts (see src/prose.ts, and the generated word table in
// src/vocabulary.json): the browser fetches it; tests read it from the source tree.
export async function readJson<T>(url: string): Promise<T> {
  if (__PROSE_FROM_DISK__) {
    const fs = "node:fs/promises";
    const { readFile } = (await import(/* @vite-ignore */ fs)) as { readFile(path: string, enc: string): Promise<string> };
    const cwd = (globalThis as unknown as { process: { cwd(): string } }).process.cwd();
    return JSON.parse(await readFile(`${cwd}${url.split("?")[0]}`, "utf8")) as T;
  }
  const res = await fetch(url);
  if (!res.ok) throw new Error(`The file did not load (${res.status}).`);
  return (await res.json()) as T;
}
