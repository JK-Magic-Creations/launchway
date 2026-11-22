import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  define: { __PROSE_FROM_DISK__: true },
  // The documentation tests read the Markdown in ../docs (src/learn/docs.test.ts).
  server: { fs: { allow: [".."] } },
  test: {
    environment: "jsdom",
    setupFiles: ["src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
