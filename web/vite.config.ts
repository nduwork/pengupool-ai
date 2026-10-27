import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The bundle is embedded by internal/webapp and served from the app://
// scheme, so no asset URL may carry a host and nothing may be fetched
// from a CDN at runtime. A relative base keeps the same HTML valid
// whatever prefix it is mounted under.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "../internal/webapp/dist",
    emptyOutDir: true,
    target: "es2022",
    sourcemap: false,
  },
});
