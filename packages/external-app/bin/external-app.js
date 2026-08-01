#!/usr/bin/env node

import createClient from "openapi-fetch";

const baseURL = (process.env.NANOFLARE_URL || "http://127.0.0.1:8080").replace(/\/+$/, "");
const email = process.env.NANOFLARE_EMAIL || "external-admin@example.com";
const password = process.env.NANOFLARE_PASSWORD || "secret";
const organizationName = process.env.NANOFLARE_ORG_NAME || "External App Test";
const redirectURI =
  process.env.EXTERNAL_APP_REDIRECT_URI || "https://external.example.com/oauth/callback";
const workerHostname = process.env.EXTERNAL_WORKER_HOSTNAME || `external-${Date.now()}.example.com`;
/** @typedef {import("@nanoflare/schema").paths} NanoflarePaths */
/** @type {ReturnType<typeof createClient<NanoflarePaths>>} */
const api = createClient({ baseUrl: baseURL });

const defaultScopes = ["workers:write", "kv:write"];
const requestedScopes = (process.env.EXTERNAL_APP_SCOPES || defaultScopes.join(" "))
  .split(/[,\s]+/)
  .map((scope) => scope.trim())
  .filter(Boolean);

async function main() {
  console.log(`Testing Nanoflare OAuth integration at ${baseURL}`);

  const session = await signIn();
  console.log(`Signed in as ${session.user.email}; org=${session.active_org_id}`);

  const client =
    process.env.EXTERNAL_APP_CLIENT_ID && process.env.EXTERNAL_APP_CLIENT_SECRET
      ? {
          client_id: process.env.EXTERNAL_APP_CLIENT_ID,
          client_secret: process.env.EXTERNAL_APP_CLIENT_SECRET,
        }
      : await createOAuthClient(session.access_token, session.active_org_id);
  console.log(`Using OAuth client ${client.client_id}`);

  const authorization = await authorizeClient(
    session.access_token,
    session.active_org_id,
    client.client_id,
  );
  console.log(`Received authorization code; redirect=${authorization.redirect_to}`);

  const token = await exchangeAuthorizationCode(client, authorization.code);
  console.log(`Received access token with scopes: ${token.scope}`);

  const app = await createManagedWorker(token.access_token, session.active_org_id);
  console.log(`Created managed worker ${app.id} (${app.hostname})`);
  console.log(
    `External metadata: external_id=${app.external_id} oauth_client_id=${app.oauth_client_id}`,
  );

  await expectMissingReadScope(token.access_token, session.active_org_id);
  console.log("Confirmed workers:read is required for listing workers");

  const refreshed = await refreshToken(client, token.refresh_token);
  console.log("Refreshed token and rotated refresh token");

  await revokeToken(refreshed.access_token);
  await expectRevokedToken(refreshed.access_token, session.active_org_id);
  console.log("Revoked token is rejected");

  console.log("External app OAuth smoke test completed successfully.");
}

async function signIn() {
  const login = await api.POST("/v1/auth/login", {
    body: { email, password },
  });
  if (login.response.status === 200 && login.data) {
    return login.data;
  }
  if (login.response.status !== 401) {
    throw requestError("POST /v1/auth/login", login.response, login.error);
  }

  const signup = await api.POST("/v1/setup/signup", {
    body: { email, password, organization_name: organizationName },
  });
  if (signup.response.status === 201 && signup.data) {
    return signup.data;
  }
  if (signup.response.status !== 409) {
    throw requestError("POST /v1/setup/signup", signup.response, signup.error);
  }

  throw new Error(
    "Could not log in or create the first Nanoflare user. Set NANOFLARE_EMAIL and NANOFLARE_PASSWORD for an existing account.",
  );
}

async function createOAuthClient(token, orgID) {
  const { data, error, response } = await api.POST("/v1/organizations/{orgID}/oauth-clients", {
    body: {
      name: "External Platform Smoke Test",
      redirect_uris: [redirectURI],
      scopes: [
        "workers:read",
        "workers:write",
        "deployments:write",
        "kv:read",
        "kv:write",
        "objects:read",
        "objects:write",
        "secrets:write",
      ],
    },
    params: { path: { orgID } },
    headers: { Authorization: `Bearer ${token}` },
  });
  if (response.status !== 201 || !data)
    throw requestError("POST /v1/organizations/{orgID}/oauth-clients", response, error);
  return data;
}

async function authorizeClient(token, orgID, clientID) {
  const { data, error, response } = await api.POST("/v1/oauth/authorize", {
    headers: { Authorization: `Bearer ${token}` },
    body: {
      client_id: clientID,
      redirect_uri: redirectURI,
      scopes: requestedScopes,
      org_id: orgID,
      state: "external-app-smoke-test",
    },
  });
  if (!response.ok || !data) throw requestError("POST /v1/oauth/authorize", response, error);
  return data;
}

async function exchangeAuthorizationCode(client, code) {
  return oauthToken(client, { grant_type: "authorization_code", code, redirect_uri: redirectURI });
}

async function createManagedWorker(accessToken, orgID) {
  const { data, error, response } = await api.POST("/v1/organizations/{orgID}/workers", {
    params: { path: { orgID } },
    headers: { Authorization: `Bearer ${accessToken}` },
    body: {
      name: "External Managed Worker",
      hostname: workerHostname,
      external_id: `external-worker-${Date.now()}`,
    },
  });
  if (response.status !== 201 || !data) throw requestError("POST /v1/workers", response, error);
  return data;
}

async function expectMissingReadScope(accessToken, orgID) {
  const { error, response } = await api.GET("/v1/organizations/{orgID}/workers", {
    params: { path: { orgID } },
    headers: { Authorization: `Bearer ${accessToken}` },
  });
  const expectedStatus = requestedScopes.includes("workers:read") ? 200 : 403;
  if (response.status !== expectedStatus) throw requestError("GET /v1/workers", response, error);
}

async function refreshToken(client, refreshTokenValue) {
  return oauthToken(client, { grant_type: "refresh_token", refresh_token: refreshTokenValue });
}

async function oauthToken(client, values) {
  const response = await fetch(`${baseURL}/v1/oauth/token`, {
    method: "POST",
    headers: {
      Authorization: `Basic ${Buffer.from(`${client.client_id}:${client.client_secret}`).toString("base64")}`,
      "Content-Type": "application/x-www-form-urlencoded",
    },
    body: new URLSearchParams(values),
  });
  const data = await response.json();
  if (!response.ok) throw requestError("POST /v1/oauth/token", response, data);
  return data;
}

async function revokeToken(token) {
  const response = await fetch(`${baseURL}/v1/oauth/revoke`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ token }),
  });
  if (response.status !== 204)
    throw requestError("POST /v1/oauth/revoke", response, await response.json());
}

async function expectRevokedToken(accessToken, orgID) {
  const { error, response } = await api.POST("/v1/organizations/{orgID}/workers", {
    params: { path: { orgID } },
    headers: { Authorization: `Bearer ${accessToken}` },
    body: {
      name: "Should Not Be Created",
      hostname: `revoked-${Date.now()}.example.com`,
    },
  });
  if (response.status !== 401) throw requestError("POST /v1/workers", response, error);
}

function requestError(operation, response, error) {
  return new Error(`${operation} returned ${response.status}: ${JSON.stringify(error)}`);
}

main().catch((error) => {
  console.error(error.message);
  process.exit(1);
});
