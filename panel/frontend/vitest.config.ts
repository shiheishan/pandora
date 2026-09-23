import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    // Bound concurrent jsdom/Ant Design trees to avoid CPU starvation in UI tests.
    maxWorkers: 2,
    environment: "jsdom",
    environmentOptions: { jsdom: { url: "http://localhost/prefix/app/" } },
    setupFiles: ["./tests/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}", "tests/**/*.test.{ts,tsx}"],
    restoreMocks: true,
  },
});
