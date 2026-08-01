import { defineConfig } from "vite";

const optionalVendorStub = new URL("./worker/openapi-optional-vendor-stub.ts", import.meta.url).pathname;

export default defineConfig({
  resolve: {
    alias: [
      { find: "effect", replacement: optionalVendorStub },
      { find: "sury", replacement: optionalVendorStub },
      { find: "zod/v4/core", replacement: optionalVendorStub },
      { find: "zod-to-json-schema", replacement: optionalVendorStub },
      { find: "zod-openapi", replacement: optionalVendorStub },
    ],
  },
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
        inlineDynamicImports: true,
      },
    },
  },
});
