/// <reference types="vitest/config" />

import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  // In local dev Vite stands in for the production nginx layer and proxies the
  // backend routes to the key-manager API. Override with WEBAPI_URL to point at
  // an in-cluster ClusterIP instead of a locally running key-manager.
  const apiTarget = env.WEBAPI_URL ?? "http://localhost:8080";

  return {
    // The Tailwind plugin is required for Tailwind v4 utilities and shadcn
    // component styles to compile in dev and build.
    plugins: [react(), tailwindcss()],

    resolve: {
      alias: {
        // shadcn emits imports like "@/components" and "@/lib/utils".
        "@": path.resolve(__dirname, "./src"),
      },
    },

    server: {
      proxy: {
        "/api": { target: apiTarget, changeOrigin: true },
        "/logout": { target: apiTarget, changeOrigin: true },
      },
    },

    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: "./src/test/setup.ts",
      css: true,
      include: ["src/**/*.{test,spec}.{ts,tsx}"],
    },
  };
});
