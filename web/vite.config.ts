import { gzipSync } from "node:zlib";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// Beside each text file of src/prose (see src/prose.ts) the build writes a gzipped copy, name.json.gz: the server sends
// that one to a browser that accepts gzip, with the content hash in the name as its ETag.
function gzipProse(): Plugin {
  let out = "dist";
  return {
    name: "launchway-gzip-prose",
    apply: "build",
    configResolved(c) { out = join(c.root, c.build.outDir); },
    writeBundle(_o, bundle) {
      for (const name of Object.keys(bundle)) {
        if (/^assets\/.+\.json$/.test(name)) writeFileSync(join(out, name + ".gz"), gzipSync(readFileSync(join(out, name)), { level: 9 }));
      }
    },
  };
}

// A module the first screen needs (reached from main.tsx by static imports alone) goes into one chunk, "core". Without
// this, pages whose modules wait for their text (top-level await, see src/prose.ts) cannot import from the entry chunk,
// so everything they share with it is cut into a dozen small chunks, each with its own import and export lists.
const staticFromEntry = new Map<string, boolean>();
function manualChunks(id: string, { getModuleInfo }: { getModuleInfo: (id: string) => { importers: readonly string[]; isEntry: boolean } | null }): string | undefined {
  const reached = (m: string, seen: Set<string>): boolean => {
    const known = staticFromEntry.get(m);
    if (known !== undefined) return known;
    if (seen.has(m)) return false;
    seen.add(m);
    const info = getModuleInfo(m);
    const yes = !!info && (info.isEntry || info.importers.some((i) => reached(i, seen)));
    staticFromEntry.set(m, yes);
    return yes;
  };
  return reached(id, new Set()) ? "core" : undefined;
}

// In development, `npm run dev` serves the UI on :5173 and sends API calls
// to a Launchway server on :8080.
export default defineConfig({
  plugins: [react(), gzipProse()],
  // Tests read the text files from disk; a browser fetches them.
  define: { __PROSE_FROM_DISK__: false },
  // experimentalMinChunkSize joins chunks under 2 kB into one that already loads with them: every chunk carries its own
  // import preamble, and 190 chunks made the total about 5 kB larger than it needs to be.
  // assetsInlineLimit: a text file is always a file of its own (the default turns small ones into data: addresses inside the script).
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, assetsInlineLimit: (file) => (file.endsWith(".json") ? false : undefined),
    rollupOptions: { output: { experimentalMinChunkSize: 2000, manualChunks } } },
  // HTML reports keep the Host header: their Content-Security-Policy names it.
  server: { proxy: { "/api": "http://127.0.0.1:8080", "/auth": "http://127.0.0.1:8080",
    "/hosted": { target: "http://127.0.0.1:8080", changeOrigin: false } } },
});
