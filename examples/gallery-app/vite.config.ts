import { cloudflare } from "@cloudflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

const optionalVendorStub = new URL("./worker/openapi-optional-vendor-stub.ts", import.meta.url)
  .pathname;

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

  plugins: [react(), tailwindcss(), cloudflare({ persistState: { path: ".wrangler/state" } })],
});
