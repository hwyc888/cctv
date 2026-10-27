import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const projectRoot = fileURLToPath(new URL("./", import.meta.url));

export default defineConfig({
  root: fileURLToPath(new URL("./standalone/", import.meta.url)),
  publicDir: fileURLToPath(new URL("./public/", import.meta.url)),
  plugins: [react()],
  css: { postcss: projectRoot },
  // The bundled Web UI sends /api requests to the same Go server.
  define: { "process.env.NEXT_PUBLIC_API_URL": "undefined" },
  build: {
    outDir: fileURLToPath(new URL("./cmd/cctv-sub/webdist/", import.meta.url)),
    emptyOutDir: true,
  },
});
