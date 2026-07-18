#!/usr/bin/env node

const baseURL = (process.env.NANOFLARE_URL || "http://127.0.0.1:8080").replace(/\/+$/, "");
const email = process.env.NANOFLARE_EMAIL || "external-admin@example.com";
const password = process.env.NANOFLARE_PASSWORD || "secret";
const organizationName = process.env.NANOFLARE_ORG_NAME || "External App Test";
const redirectURI = process.env.EXTERNAL_APP_REDIRECT_URI || "https://external.example.com/oauth/callback";
const workerHostname = process.env.EXTERNAL_WORKER_HOSTNAME || `external-${Date.now()}.example.com`;

const defaultScopes = ["apps:write", "kv:write"];
const requestedScopes = (process.env.EXTERNAL_APP_SCOPES || defaultScopes.join(" "))
  .split(/[,\s]+/)
  .map((scope) => scope.trim())
  .filter(Boolean);

async function main() {
  console.log(`Testing Nanoflare OAuth integration at ${baseURL}`);

  const session = await signIn();
  console.log(`Signed in as ${session.user.email}; org=${session.active_org_id}`);

  const client = process.env.EXTERNAL_APP_CLIENT_ID && process.env.EXTERNAL_APP_CLIENT_SECRET
    ? { client_id: process.env.EXTERNAL_APP_CLIENT_ID, client_secret: process.env.EXTERNAL_APP_CLIENT_SECRET }
    : await createOAuthClient(session.token, session.active_org_id);
  console.log(`Using OAuth client ${client.client_id}`);

  const authorization = await authorizeClient(session.token, session.active_org_id, client.client_id);
  console.log(`Received authorization code; redirect=${authorization.redirect_to}`);

  const token = await exchangeAuthorizationCode(client, authorization.code);
  console.log(`Received access token with scopes: ${token.scope}`);

  const app = await createManagedWorker(token.access_token);
  console.log(`Created managed worker ${app.id} (${app.hostname})`);
  console.log(`External metadata: external_id=${app.external_id} oauth_client_id=${app.oauth_client_id}`);

  await expectMissingReadScope(token.access_token);
  console.log("Confirmed apps:read is required for listing apps");

  const refreshed = await refreshToken(client, token.refresh_token);
  console.log("Refreshed token and rotated refresh token");

  await revokeToken(refreshed.access_token);
  await expectRevokedToken(refreshed.access_token);
  console.log("Revoked token is rejected");

  console.log("External app OAuth smoke test completed successfully.");
}

async function signIn() {
  const login = await request("POST", "/v1/auth/login", {
    body: { email, password },
    allowStatus: [200, 401],
  });
  if (login.status === 200) {
    return login.json;
  }

  const signup = await request("POST", "/v1/setup/signup", {
    body: { email, password, organization_name: organizationName },
    allowStatus: [201, 409],
  });
  if (signup.status === 201) {
    return signup.json;
  }

  throw new Error("Could not log in or create the first Nanoflare user. Set NANOFLARE_EMAIL and NANOFLARE_PASSWORD for an existing account.");
}

async function createOAuthClient(token, orgID) {
  const response = await request("POST", "/v1/oauth/clients", {
    token,
    headers: { "X-Nanoflare-Org-ID": orgID },
    body: {
      name: "External Platform Smoke Test",
      redirect_uris: [redirectURI],
      scopes: ["apps:read", "apps:write", "deployments:write", "kv:read", "kv:write", "objects:read", "objects:write", "secrets:write"],
    },
    wantStatus: 201,
  });
  return response.json;
}

async function authorizeClient(token, orgID, clientID) {
  const response = await request("POST", "/v1/oauth/authorize", {
    token,
    body: {
      client_id: clientID,
      redirect_uri: redirectURI,
      scopes: requestedScopes,
      org_id: orgID,
      state: "external-app-smoke-test",
    },
    wantStatus: 200,
  });
  return response.json;
}

async function exchangeAuthorizationCode(client, code) {
  const response = await request("POST", "/v1/oauth/token", {
    body: {
      grant_type: "authorization_code",
      client_id: client.client_id,
      client_secret: client.client_secret,
      code,
      redirect_uri: redirectURI,
    },
    wantStatus: 200,
  });
  return response.json;
}

async function createManagedWorker(accessToken) {
  const response = await request("POST", "/v1/apps", {
    token: accessToken,
    body: {
      name: "External Managed Worker",
      hostname: workerHostname,
      external_id: `external-worker-${Date.now()}`,
    },
    wantStatus: 201,
  });
  return response.json;
}

async function expectMissingReadScope(accessToken) {
  await request("GET", "/v1/apps", {
    token: accessToken,
    wantStatus: requestedScopes.includes("apps:read") ? 200 : 403,
  });
}

async function refreshToken(client, refreshTokenValue) {
  const response = await request("POST", "/v1/oauth/token", {
    body: {
      grant_type: "refresh_token",
      client_id: client.client_id,
      client_secret: client.client_secret,
      refresh_token: refreshTokenValue,
    },
    wantStatus: 200,
  });
  return response.json;
}

async function revokeToken(token) {
  await request("POST", "/v1/oauth/revoke", {
    body: { token },
    wantStatus: 204,
  });
}

async function expectRevokedToken(accessToken) {
  await request("POST", "/v1/apps", {
    token: accessToken,
    body: {
      name: "Should Not Be Created",
      hostname: `revoked-${Date.now()}.example.com`,
    },
    wantStatus: 401,
  });
}

async function request(method, path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  if (options.token) {
    headers.set("Authorization", `Bearer ${options.token}`);
  }

  const response = await fetch(`${baseURL}${path}`, {
    method,
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });

  const text = await response.text();
  const json = text ? JSON.parse(text) : undefined;
  const allowed = options.allowStatus || [options.wantStatus || 200];
  if (!allowed.includes(response.status)) {
    throw new Error(`${method} ${path} returned ${response.status}: ${text}`);
  }
  return { status: response.status, json };
}

main().catch((error) => {
  console.error(error.message);
  process.exit(1);
});
