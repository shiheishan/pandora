import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const base = fileURLToPath(new URL(".", import.meta.url));

export default defineConfig(({ mode }) => {
  const app = mode === "portal" ? "portal" : "admin";
  const env = loadEnv(mode, base, "VITE_");
  return {
    root: resolve(base, "apps", app),
    envDir: base,
    base: "./",
    plugins: [react()],
    build: {
      outDir: resolve(base, "dist", app),
      emptyOutDir: true,
      manifest: true,
      sourcemap: false,
      target: "es2022",
    },
    server: {
      host: "127.0.0.1",
      fs: { allow: [base] },
      proxy: {
        "/v1": {
          target:
            env.VITE_PROXY_TARGET ||
            (app === "admin"
              ? "http://127.0.0.1:9001"
              : "http://127.0.0.1:9000"),
          changeOrigin: false,
        },
      },
    },
  };
});
