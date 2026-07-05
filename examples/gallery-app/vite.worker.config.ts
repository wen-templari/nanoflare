import { defineConfig } from "vite";

export default defineConfig({
  build: {
    outDir: "dist",
    emptyOutDir: false,
    copyPublicDir: false,
    minify: false,
    lib: {
      entry: "worker/index.ts",
      formats: ["es"],
      fileName: () => "worker.js",
    },
    rollupOptions: {
      output: {
        entryFileNames: "worker.js",
      },
    },
  },
});
