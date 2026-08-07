import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { svelteTesting } from "@testing-library/svelte/vite";

// Build into ../internal/server/dist so the Go server package can embed it
// via //go:embed all:dist (Go embed paths are relative to the source file).
// The dev server runs on 5173 and proxies /api to the backend on 8080.
export default defineConfig({
	plugins: [svelte(), svelteTesting()],
	build: {
		outDir: "../internal/server/dist",
		emptyOutDir: true,
	},
	server: {
		proxy: {
			"/api": "http://localhost:8080",
		},
	},
	test: {
		// jsdom so component tests have a DOM to render into.
		environment: "jsdom",
		include: ["src/**/*.test.ts"],
		environmentOptions: {
			jsdom: {
				url: "http://localhost",
			},
		},
	},
});
