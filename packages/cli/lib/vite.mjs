import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

function modulePath(path) {
  try {
    path = realpathSync(path);
  } catch {
    /* Vite may create an entry after config resolution. */
  }
  return path.replaceAll("\\", "/");
}

// Works alongside the official Cloudflare Vite plugin. Restrict only the Worker
// entry selected by the developer, never browser modules or unrelated Workers.
export function privateDynamicProfile({ entry }) {
  let workerEntry;
  const profile = fileURLToPath(
    new URL("./runtime-profile-bootstrap.mjs", import.meta.url),
  ).replaceAll("\\", "/");
  return {
    name: "nanoflare:private-dynamic-profile",
    enforce: "pre",
    configResolved(config) {
      workerEntry = modulePath(resolve(config.root, entry));
    },
    transform(code, id) {
      if (modulePath(id.split("?")[0]) !== workerEntry) return;
      return { code: `import ${JSON.stringify(profile)};\n${code}`, map: null };
    },
  };
}
