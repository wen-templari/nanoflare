import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "");
  const platformd = env.PLATFORMD_URL || "http://127.0.0.1:8080";
  return {
    plugins: [react(), tailwindcss()],
    server: {
      proxy: {
        "/v1": platformd,
        "/healthz": platformd,
        "/prometheus": {
          target: "http://127.0.0.1:9090",
          rewrite: (path) => path.replace(/^\/prometheus/, ""),
        },
      },
    },
  };
});
