import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

// The build output is what go:embed packages (internal/webui/dist), so that a
// single go build produces the single self-contained binary.
// emptyOutDir stays false: scripts/build-web.ps1 owns the cleanup so that the
// tracked .gitkeep placeholder survives.
export default defineConfig({
  plugins: [preact()],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: false,
    target: "es2020",
    sourcemap: false,
    chunkSizeWarningLimit: 800,
    // lightningcss (Vite 8's default CSS minifier) rejects a selector quirk in
    // xp.css ("progress:not([value]):before:not([value])"). Keep the CSS as-is;
    // the Go server can gzip it on the wire instead.
    cssMinify: false,
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/onebot": { target: "http://127.0.0.1:8080", ws: true },
      "/healthz": { target: "http://127.0.0.1:8080" },
      "/metrics": { target: "http://127.0.0.1:8080" },
    },
  },
});
