#!/usr/bin/env node

import http from "node:http";
import crypto from "node:crypto";

const nanoflareURL = (process.env.NANOFLARE_URL || "http://127.0.0.1:8080").replace(/\/+$/, "");
const nanoflareUIURL = (process.env.NANOFLARE_UI_URL || "http://127.0.0.1:5173").replace(/\/+$/, "");
const port = Number(process.env.EXTERNAL_APP_PORT || 8787);
const externalOrigin = (process.env.EXTERNAL_APP_ORIGIN || `http://127.0.0.1:${port}`).replace(/\/+$/, "");
const redirectURI = `${externalOrigin}/oauth/callback`;
const scopes = (process.env.EXTERNAL_APP_SCOPES || "apps:write kv:write")
  .split(/[,\s]+/)
  .map((scope) => scope.trim())
  .filter(Boolean);

const state = {
  client: process.env.EXTERNAL_APP_CLIENT_ID && process.env.EXTERNAL_APP_CLIENT_SECRET
    ? { client_id: process.env.EXTERNAL_APP_CLIENT_ID, client_secret: process.env.EXTERNAL_APP_CLIENT_SECRET }
    : null,
  accessToken: "",
  refreshToken: "",
  tokenScope: "",
  tokenType: "",
  tokenIssuedAt: null,
  tokenExpiresAt: null,
  lastApp: null,
  events: [],
};

const server = http.createServer(async (request, response) => {
  try {
    const url = new URL(request.url || "/", externalOrigin);
    if (request.method === "GET" && url.pathname === "/") {
      return html(response, page());
    }
    if (request.method === "POST" && url.pathname === "/connect") {
      await ensureOAuthClient();
      const oauthURL = new URL("/oauth/authorize", nanoflareUIURL);
      oauthURL.searchParams.set("response_type", "code");
      oauthURL.searchParams.set("client_id", state.client.client_id);
      oauthURL.searchParams.set("redirect_uri", redirectURI);
      oauthURL.searchParams.set("scope", scopes.join(" "));
      oauthURL.searchParams.set("state", nonce());
      logEvent("Redirecting user to Nanoflare for consent.");
      return redirect(response, oauthURL.toString());
    }
    if (request.method === "GET" && url.pathname === "/oauth/callback") {
      await exchangeCode(url.searchParams.get("code") || "");
      logEvent("External app received OAuth callback and exchanged code.");
      return redirect(response, "/");
    }
    if (request.method === "POST" && url.pathname === "/provision") {
      const body = await readForm(request);
      await provisionWorker(body);
      return redirect(response, "/");
    }
    if (request.method === "POST" && url.pathname === "/refresh") {
      await refreshToken();
      logEvent("External app refreshed and rotated OAuth tokens.");
      return redirect(response, "/");
    }
    if (request.method === "POST" && url.pathname === "/revoke") {
      await revokeToken();
      logEvent("External app revoked the current access token.");
      return redirect(response, "/");
    }
    response.writeHead(404).end("not found");
  } catch (error) {
    logEvent(error.message);
    html(response, page(error.message), 500);
  }
});

server.listen(port, "127.0.0.1", () => {
  console.log(`External app UI: ${externalOrigin}`);
  console.log(`Nanoflare URL: ${nanoflareURL}`);
  console.log(`Nanoflare UI URL: ${nanoflareUIURL}`);
});

async function ensureOAuthClient() {
  if (state.client) return;
  throw new Error("External app is not registered in Nanoflare. Start this app with EXTERNAL_APP_CLIENT_ID and EXTERNAL_APP_CLIENT_SECRET, then click Connect Nanoflare.");
}

async function exchangeCode(code) {
  if (!code) throw new Error("Nanoflare callback did not include a code.");
  if (!state.client) throw new Error("OAuth client was not initialized.");
  const response = await nf("POST", "/v1/oauth/token", {
    grant_type: "authorization_code",
    client_id: state.client.client_id,
    client_secret: state.client.client_secret,
    code,
    redirect_uri: redirectURI,
  }, { wantStatus: 200 });
  state.accessToken = response.json.access_token;
  state.refreshToken = response.json.refresh_token;
  state.tokenScope = response.json.scope;
  state.tokenType = response.json.token_type;
  state.tokenIssuedAt = new Date();
  state.tokenExpiresAt = new Date(state.tokenIssuedAt.getTime() + Number(response.json.expires_in || 0) * 1000);
}

async function provisionWorker(form) {
  if (!state.accessToken) throw new Error("Connect Nanoflare before provisioning.");
  const name = form.get("name") || "External Managed Worker";
  const suffix = Date.now();
  const hostname = `external-${suffix}.example.com`;
  const externalID = `external-worker-${suffix}`;
  const response = await nf("POST", "/v1/apps", {
    name,
    hostname,
    external_id: externalID,
  }, { token: state.accessToken, wantStatus: 201 });
  state.lastApp = response.json;
  logEvent(`Provisioned worker ${response.json.id}.`);
}

async function refreshToken() {
  if (!state.refreshToken || !state.client) throw new Error("No refresh token is available.");
  const response = await nf("POST", "/v1/oauth/token", {
    grant_type: "refresh_token",
    client_id: state.client.client_id,
    client_secret: state.client.client_secret,
    refresh_token: state.refreshToken,
  }, { wantStatus: 200 });
  state.accessToken = response.json.access_token;
  state.refreshToken = response.json.refresh_token;
  state.tokenScope = response.json.scope;
  state.tokenType = response.json.token_type;
  state.tokenIssuedAt = new Date();
  state.tokenExpiresAt = new Date(state.tokenIssuedAt.getTime() + Number(response.json.expires_in || 0) * 1000);
}

async function revokeToken() {
  if (!state.accessToken) throw new Error("No access token is available.");
  await nf("POST", "/v1/oauth/revoke", { token: state.accessToken }, { wantStatus: 204 });
  state.accessToken = "";
  state.refreshToken = "";
  state.tokenScope = "";
  state.tokenType = "";
  state.tokenIssuedAt = null;
  state.tokenExpiresAt = null;
}

async function nf(method, path, body, options = {}) {
  const headers = new Headers();
  if (body !== undefined) headers.set("Content-Type", "application/json");
  if (options.token) headers.set("Authorization", `Bearer ${options.token}`);
  const response = await fetch(`${nanoflareURL}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  const json = text ? JSON.parse(text) : undefined;
  const allowed = options.allowStatus || [options.wantStatus || 200];
  if (!allowed.includes(response.status)) {
    throw new Error(`${method} ${path} returned ${response.status}: ${text}`);
  }
  return { status: response.status, json };
}

async function readForm(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return new URLSearchParams(Buffer.concat(chunks).toString("utf8"));
}

function redirect(response, location) {
  response.writeHead(303, { Location: location });
  response.end();
}

function html(response, content, status = 200) {
  response.writeHead(status, { "Content-Type": "text/html; charset=utf-8" });
  response.end(content);
}

function logEvent(message) {
  state.events.unshift({ time: new Date().toLocaleTimeString(), message });
  state.events = state.events.slice(0, 6);
}

function nonce() {
  return crypto.randomBytes(12).toString("hex");
}

function page(error = "") {
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>External Platform</title>
<style>
:root{--bg:#f7f8fa;--panel:#ffffff;--ink:#17202a;--muted:#667085;--line:#d9dee7;--blue:#2563eb;--green:#15803d;--red:#b42318;--code:#f3f5f8}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;padding:32px}
.shell{max-width:960px;margin:0 auto;display:grid;gap:18px}.hero{display:grid;gap:8px}.brand{font-size:13px;font-weight:700;color:var(--blue);text-transform:uppercase}h1{font-size:32px;line-height:1.15;margin:0;letter-spacing:0}p{margin:0;color:var(--muted);line-height:1.5}
.meta-line{display:flex;flex-wrap:wrap;gap:10px;color:var(--muted);font-size:13px}.grid{display:grid;gap:14px}.panel{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:18px}.panel.dark{background:var(--panel);color:var(--ink)}
.step{display:flex;justify-content:space-between;gap:16px;align-items:flex-start}.step h2{font-size:18px;margin:0 0 6px}.step-number{display:grid;place-items:center;flex:0 0 30px;height:30px;border-radius:999px;background:#eaf1ff;color:var(--blue);font-weight:800}
.content{flex:1}.meta{display:flex;flex-wrap:wrap;gap:8px;margin-top:12px;font-size:13px;color:var(--muted)}.pill{display:inline-block;border:1px solid var(--line);border-radius:999px;padding:4px 8px;margin:6px 6px 0 0;font-size:12px;color:var(--muted)}
.ok{color:var(--green)}.no{color:var(--red)}form{margin-top:12px}.fields{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px}label{display:block;margin:0 0 5px;font-size:12px;font-weight:700;color:var(--muted)}input{width:100%;height:38px;background:#fff;border:1px solid var(--line);border-radius:6px;padding:0 10px;font-size:14px;color:var(--ink)}
.actions{display:flex;flex-wrap:wrap;gap:10px;margin-top:12px}button{height:38px;border:0;border-radius:6px;background:var(--blue);color:#fff;padding:0 14px;font-weight:700;cursor:pointer}button.secondary{background:#eef2f7;color:var(--ink)}button.danger{background:#fee4e2;color:var(--red)}
.details{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;margin-top:12px}.detail{background:var(--code);border-radius:6px;padding:10px}.detail span{display:block;color:var(--muted);font-size:12px;margin-bottom:4px}.detail code{font-size:13px;word-break:break-all}
.events{list-style:none;padding:0;margin:0;display:grid;gap:8px}.events li{font-size:13px;color:var(--muted);border-top:1px solid var(--line);padding-top:8px}.error{border:1px solid #fda29b;background:#fff1f0;border-radius:8px;padding:12px;font-size:13px;font-weight:700;color:var(--red)}
@media(max-width:760px){body{padding:18px}.fields{grid-template-columns:1fr}.step{display:grid}.step-number{margin-bottom:4px}}
</style>
</head>
<body><main class="shell">
<section class="hero"><div class="brand">External Platform Test App</div><h1>Connect Nanoflare and create a managed worker</h1><p>A minimal UI for testing the full OAuth redirect flow from a registered external app. User authentication happens in the Nanoflare UI, not here.</p><div class="meta-line"><span>Nanoflare API: ${escapeHTML(nanoflareURL)}</span><span>Nanoflare UI: ${escapeHTML(nanoflareUIURL)}</span><span>Callback: ${escapeHTML(redirectURI)}</span></div></section>
${error ? `<div class="error">${escapeHTML(error)}</div>` : ""}
<section class="grid">
<div class="panel"><div class="step"><div class="step-number">1</div><div class="content"><h2>Connect Nanoflare</h2><p>This redirects to Nanoflare for login and approval, then returns to this app.</p><form method="post" action="/connect"><div class="actions"><button type="submit">Connect Nanoflare</button></div></form><div>${scopes.map((scope) => `<span class="pill">${escapeHTML(scope)}</span>`).join("")}</div><div class="meta"><span class="${state.client ? "ok" : "no"}">${state.client ? "External app registered" : "Missing EXTERNAL_APP_CLIENT_ID / EXTERNAL_APP_CLIENT_SECRET"}</span></div></div></div></div>
<div class="panel"><div class="step"><div class="step-number">2</div><div class="content"><h2>Create worker from external app</h2><p>The external app chooses hostname and external ID internally, then calls Nanoflare with its OAuth token.</p><form method="post" action="/provision"><div class="fields">
<div><label>Worker name</label><input name="name" value="External Managed Worker"></div>
</div><div class="actions"><button type="submit">Create worker</button></div></form>${state.lastApp ? `<div class="meta"><span>App: ${escapeHTML(state.lastApp.id)}</span><span>Host: ${escapeHTML(state.lastApp.hostname)}</span><span>External ID: ${escapeHTML(state.lastApp.external_id || "")}</span><span>Owner: ${escapeHTML(state.lastApp.oauth_client_id || "")}</span></div>` : ""}</div></div></div>
<div class="panel"><div class="step"><div class="step-number">3</div><div class="content"><h2>Token actions</h2><div class="meta"><span class="${state.accessToken ? "ok" : "no"}">${state.accessToken ? "Access token stored" : "No access token"}</span><span>Scopes: ${escapeHTML(state.tokenScope || "none")}</span></div><div class="actions"><form method="post" action="/refresh"><button class="secondary" type="submit">Refresh token</button></form><form method="post" action="/revoke"><button class="danger" type="submit">Revoke token</button></form></div></div></div></div>
<div class="panel"><div class="step"><div class="step-number">4</div><div class="content"><h2>Token details</h2><p>Nanoflare access tokens are opaque, so this shows the token response metadata the external app can safely inspect.</p>${tokenDetailsHTML()}</div></div></div>
</section>
<section class="panel"><h2>Event log</h2><ul class="events">${state.events.map((event) => `<li>${escapeHTML(event.time)} - ${escapeHTML(event.message)}</li>`).join("") || "<li>No events yet.</li>"}</ul></section>
</main></body></html>`;
}

function tokenDetailsHTML() {
  const expiresAt = state.tokenExpiresAt instanceof Date ? state.tokenExpiresAt : null;
  const issuedAt = state.tokenIssuedAt instanceof Date ? state.tokenIssuedAt : null;
  const now = Date.now();
  const secondsRemaining = expiresAt ? Math.max(0, Math.round((expiresAt.getTime() - now) / 1000)) : 0;
  const rows = [
    ["Status", state.accessToken ? "active" : "not connected"],
    ["Token type", state.tokenType || "none"],
    ["Client ID", state.client?.client_id || "not configured"],
    ["Granted scopes", state.tokenScope || "none"],
    ["Issued at", issuedAt ? issuedAt.toLocaleString() : "none"],
    ["Expires at", expiresAt ? expiresAt.toLocaleString() : "none"],
    ["Seconds remaining", state.accessToken ? String(secondsRemaining) : "0"],
    ["Access token", maskToken(state.accessToken)],
    ["Refresh token", maskToken(state.refreshToken)],
  ];
  return `<div class="details">${rows.map(([label, value]) => `<div class="detail"><span>${escapeHTML(label)}</span><code>${escapeHTML(value)}</code></div>`).join("")}</div>`;
}

function maskToken(token) {
  if (!token) return "none";
  if (token.length <= 14) return token;
  return `${token.slice(0, 8)}...${token.slice(-6)}`;
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[char]));
}
