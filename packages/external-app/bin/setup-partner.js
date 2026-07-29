#!/usr/bin/env node

import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const authStore = process.env.NANOFLARE_AUTH_STORE || defaultAuthStore();
const integrationName = process.env.NANOFLARE_PARTNER_NAME || process.argv[2] || "External App UI";
const allowedScopes = (process.env.NANOFLARE_PARTNER_SCOPES || "workers:read workers:write deployments:write secrets:write kv:read kv:write db:read db:write objects:read objects:write")
  .split(/[,\s]+/)
  .map((scope) => scope.trim())
  .filter(Boolean);

const auth = JSON.parse(await fs.readFile(authStore, "utf8"));
if (!auth.api_url || !auth.token || !auth.active_org_id) {
  throw new Error(`Auth store ${authStore} must contain api_url, token, and active_org_id. Run ./bin/nanoflare auth login first.`);
}

const response = await fetch(`${auth.api_url.replace(/\/$/, "")}/v1/partner-integrations`, {
  method: "POST",
  headers: {
    Authorization: `Bearer ${auth.token}`,
    "X-Nanoflare-Org-ID": auth.active_org_id,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ name: integrationName, allowed_scopes: allowedScopes }),
});
const text = await response.text();
if (!response.ok) throw new Error(`Creating partner integration failed (${response.status}): ${text}`);
const integration = JSON.parse(text);

console.log("Partner integration created for the CLI active organization.");
console.log("Store this one-time secret in your server-side secret manager:");
console.log("");
console.log(`export NANOFLARE_PARTNER_INTEGRATION_ID=${shellQuote(integration.id)}`);
console.log(`export NANOFLARE_PARTNER_SECRET=${shellQuote(integration.secret)}`);

function shellQuote(value) {
  return `'${String(value).replaceAll("'", "'\\''")}'`;
}

function defaultAuthStore() {
  if (process.env.XDG_CONFIG_HOME) return path.join(process.env.XDG_CONFIG_HOME, "nanoflare", "auth.json");
  if (process.platform === "darwin") return path.join(os.homedir(), "Library", "Application Support", "nanoflare", "auth.json");
  if (process.platform === "win32") return path.join(process.env.APPDATA || path.join(os.homedir(), "AppData", "Roaming"), "nanoflare", "auth.json");
  return path.join(os.homedir(), ".config", "nanoflare", "auth.json");
}
