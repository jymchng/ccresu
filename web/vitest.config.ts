import path from "node:path";

import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

/**
 * Component tests run in jsdom against the real components, not snapshots, so
 * a broken link, a missing label or a non-functional toggle fails the build.
 */
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, ".") },
  },
  // Component tests assert markup and behaviour, never computed styles, so the
  // PostCSS/Tailwind pipeline is skipped: the assertions stay fast and do not
  // depend on the CSS toolchain loading in the test runner.
  css: { postcss: { plugins: [] } },
  test: {
    environment: "jsdom",
    globals: false,
    setupFiles: ["./vitest.setup.ts"],
    include: ["tests/**/*.test.{ts,tsx}"],
    // Rendering nine documentation pages in one test is CPU bound in jsdom.
    testTimeout: 20000,
    restoreMocks: true,
  },
});
