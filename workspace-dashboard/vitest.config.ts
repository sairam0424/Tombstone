import { defineConfig, configDefaults } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
    // Every real source file under src/ is .ts/.tsx (ESM `.js` import
    // specifiers resolve to them via the bundler moduleResolution mode) --
    // there is no legitimate .js here. A stray `tsc` invocation without
    // this project's `noEmit: true` respected can leave compiled .js
    // twins sitting next to their .tsx/.ts sources; left in place, Vitest
    // silently discovers and runs both, doubling every test count without
    // any signal that half the run is against a dead compiled snapshot
    // instead of live source.
    exclude: [...configDefaults.exclude, "src/**/*.js"],
  },
});
