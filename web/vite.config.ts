import { defineConfig } from "vite";

export default defineConfig({
  // During development, the dashboard runs here and forwards API calls to
  // the Go server, so the browser still sees one origin.
  server: {
    port: 3000,
    strictPort: true,
    proxy: { "/api": "http://localhost:8080" },
  },
  // The polyfill would be an inline script, which the CSP forbids.
  build: { modulePreload: { polyfill: false } },
});
