import { defineConfig } from "vite";
export default defineConfig({
  build: {
    lib: { entry: "src/worker.ts", formats: ["es"], fileName: () => "worker.js" },
    outDir: "dist",
    emptyOutDir: false,
    rollupOptions: { output: { inlineDynamicImports: true } },
  },
});
