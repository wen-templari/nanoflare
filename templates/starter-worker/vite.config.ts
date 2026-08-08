import { defineConfig } from "vite";
export default defineConfig({ build: { lib: { entry: "worker.ts", formats: ["es"], fileName: () => "worker.js" }, outDir: "dist", rollupOptions: { output: { inlineDynamicImports: true } } } });
