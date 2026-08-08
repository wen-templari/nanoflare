import { nanoflare } from "@nanoflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    nanoflare({
      include: (request) => request.url?.startsWith("/api/") ?? false,
      d1: { persist: ".nanoflare/d1" },
      r2: { persist: ".nanoflare/r2" },
    }),
  ],
  build: {
    outDir: "dist/client",
    emptyOutDir: true,
  },
});
