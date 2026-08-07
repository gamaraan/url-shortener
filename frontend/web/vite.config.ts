import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// Build into ../internal/server/dist so the Go server package can embed it
// via //go:embed all:dist (Go embed paths are relative to the source file).
// The dev server runs on 5173 and proxies /api to the backend on 8080.
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: "../internal/server/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});