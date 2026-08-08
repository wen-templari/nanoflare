import { nanoflare } from "@nanoflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
export default defineConfig({
  plugins: [tailwindcss(), nanoflare({ entry: "src/worker.tsx" })],
  build: {
    lib: { entry: "src/worker.tsx", formats: ["es"], fileName: () => "worker.js" },
    outDir: "dist",
    rollupOptions: { output: { inlineDynamicImports: true } },
  },
});
