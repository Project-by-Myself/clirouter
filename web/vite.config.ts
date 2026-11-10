import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const target = "http://127.0.0.1:8317";
const proxy = Object.fromEntries(
  ["/admin", "/v1", "/v1beta", "/healthz", "/metrics"].map((p) => [p, { target, changeOrigin: false }]),
);

export default defineConfig({
  base: "/",
  plugins: [react()],
  server: { proxy },
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 600 },
});
