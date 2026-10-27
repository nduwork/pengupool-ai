import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The bundle is embedded into the daemon by internal/webapp/embed.go and served from "/", so no
// asset URL may carry a host and nothing may be fetched from a CDN at runtime. A relative base
// keeps the same HTML valid whatever prefix it is mounted under.
const gateway = process.env.PENGUPOOL_GATEWAY ?? "http://127.0.0.1:7788";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "./",
  build: {
    outDir: "../internal/webapp/dist",
    emptyOutDir: true,
    target: "es2022",
    sourcemap: false,
  },
  server: {
    // The logo lives in ../assets, outside this root.
    fs: { allow: [".."] },
    // Development only: the production bundle talks to the daemon at its own origin.
    proxy: {
      "/ws": { target: gateway, ws: true, changeOrigin: true },
      "/api": { target: gateway, changeOrigin: true },
    },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
