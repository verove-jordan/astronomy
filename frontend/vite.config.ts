/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  // Unit tests (vitest + happy-dom); specs are colocated as <Name>.spec.ts per the house convention.
  test: {
    environment: "happy-dom",
    include: ["src/**/*.spec.ts"],
  },
  server: {
    port: Number(process.env.WEB_PORT) || 5173,
    host: true,
    // Same-origin dev: proxy /api to the host-run Go engine so the browser talks to one origin (mirrors
    // the container's nginx /api proxy). This is why BASE defaults to "" — tile <img> loads and SSE need
    // no CORS. http-proxy streams responses (no buffering), so the job/agent SSE endpoints stay live.
    proxy: {
      "/api": {
        target: process.env.VITE_DEV_API_TARGET || "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
});
