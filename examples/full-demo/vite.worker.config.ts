export default {
  build: {
    outDir: "dist",
    emptyOutDir: false,
    copyPublicDir: false,
    minify: false,
    lib: {
      entry: "src/worker.ts",
      formats: ["es"],
      fileName: () => "worker.js",
    },
    rollupOptions: {
      output: {
        entryFileNames: "worker.js",
      },
    },
  },
}
