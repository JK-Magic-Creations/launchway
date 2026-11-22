import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development, `npm run dev` serves the UI on :5173 and sends API calls
// to a Launchway server on :8080.
export default defineConfig({
  plugins: [react()],
  // experimentalMinChunkSize joins chunks under 2 kB into one that already loads with them: every chunk carries its own
  // import preamble, and 190 chunks made the total about 5 kB larger than it needs to be.
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, rollupOptions: { output: { experimentalMinChunkSize: 2000 } } },
  // HTML reports keep the Host header: their Content-Security-Policy names it.
  server: { proxy: { "/api": "http://127.0.0.1:8080", "/auth": "http://127.0.0.1:8080",
    "/hosted": { target: "http://127.0.0.1:8080", changeOrigin: false } } },
});
