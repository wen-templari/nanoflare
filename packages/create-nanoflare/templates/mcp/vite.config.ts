import { nanoflare } from "@nanoflare/vite-plugin";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [nanoflare()],
  ssr: { noExternal: true },
});
