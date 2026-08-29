import {
  AuthorizationError,
  OAuthProvider,
  type AuthRequest,
  type OAuthHelpers,
} from "@cloudflare/workers-oauth-provider";
import { createMcpHandler, McpServer } from "@modelcontextprotocol/server";
import { WorkerEntrypoint } from "cloudflare:workers";
import * as z from "zod/v4";

const ECHO_SCOPE = "echo:use";

interface AuthProps {
  userId: string;
  displayName: string;
  scopes: string[];
}

interface OAuthKVNamespace {
  get(key: string): Promise<string | null>;
  put(key: string, value: string | ArrayBuffer | ArrayBufferView | ReadableStream): Promise<void>;
  delete(key: string): Promise<void>;
}

interface Env {
  OAUTH_KV: OAuthKVNamespace;
  OAUTH_PROVIDER: OAuthHelpers;
}

function createServer(user: AuthProps): McpServer {
  const server = new McpServer(
    { name: "nanoflare-echo", version: "1.0.0" },
    { capabilities: { tools: {} } },
  );

  server.registerTool(
    "echo",
    {
      description: "Echo a message.",
      inputSchema: z.object({
        message: z.string().describe("Message to echo"),
      }),
    },
    async ({ message }) => ({
      content: [{ type: "text" as const, text: message }],
      _meta: { user: user.displayName },
    }),
  );

  return server;
}

export class EchoMcpHandler extends WorkerEntrypoint<Env, AuthProps> {
  async fetch(request: Request): Promise<Response> {
    if (!this.ctx.props.scopes.includes(ECHO_SCOPE)) {
      return new Response("The access token does not grant echo:use.", { status: 403 });
    }

    return createMcpHandler(() => createServer(this.ctx.props)).fetch(request);
  }
}

const defaultHandler: ExportedHandler<Env> = {
  async fetch(request, env) {
    const url = new URL(request.url);

    if (url.pathname === "/") {
      return Response.json({
        name: "Nanoflare OAuth MCP echo server",
        mcp_endpoint: new URL("/mcp", url).toString(),
        authorization: "OAuth 2.1 with PKCE",
      });
    }

    if (url.pathname !== "/authorize") {
      return new Response("Not found", { status: 404 });
    }

    let oauthRequest: AuthRequest;
    try {
      oauthRequest = await env.OAUTH_PROVIDER.parseAuthRequest(request);
    } catch (error) {
      return authorizationErrorResponse(error);
    }

    const client = await env.OAUTH_PROVIDER.lookupClient(oauthRequest.clientId);
    if (!client) return new Response("Unknown OAuth client", { status: 400 });

    const scopes = echoScopes(oauthRequest.scope);
    if (request.method === "GET") {
      return consentPage(url, client.clientName, scopes, oauthRequest.redirectUri);
    }

    if (request.method !== "POST") {
      return new Response("Method not allowed", {
        status: 405,
        headers: { Allow: "GET, POST" },
      });
    }

    const form = await request.formData();
    if (form.get("decision") !== "approve") {
      const redirect = new URL(oauthRequest.redirectUri);
      redirect.searchParams.set("error", "access_denied");
      redirect.searchParams.set("error_description", "The user denied the request.");
      if (oauthRequest.state) redirect.searchParams.set("state", oauthRequest.state);
      if (oauthRequest.issuer) redirect.searchParams.set("iss", oauthRequest.issuer);
      return Response.redirect(redirect, 302);
    }

    const user = { id: "demo-user", displayName: "Demo User" };
    const { redirectTo } = await env.OAUTH_PROVIDER.completeAuthorization({
      request: oauthRequest,
      userId: user.id,
      metadata: { clientName: client.clientName },
      scope: scopes,
      props: {
        userId: user.id,
        displayName: user.displayName,
        scopes,
      },
    });

    return Response.redirect(redirectTo, 302);
  },
};

function authorizationErrorResponse(error: unknown): Response {
  if (!(error instanceof AuthorizationError)) throw error;
  if (!error.redirectUri) return new Response(error.description, { status: 400 });

  const redirect = new URL(error.redirectUri);
  redirect.searchParams.set("error", error.code);
  redirect.searchParams.set("error_description", error.description);
  if (error.state) redirect.searchParams.set("state", error.state);
  if (error.issuer) redirect.searchParams.set("iss", error.issuer);
  return Response.redirect(redirect, 302);
}

function consentPage(
  url: URL,
  clientName: string | undefined,
  scopes: string[],
  redirectUri: string,
): Response {
  const requestedScopes = scopes.length > 0 ? scopes.map(escapeHtml).join(", ") : "none";
  const action = escapeHtml(`${url.pathname}${url.search}`);
  const redirectOrigin = cspOrigin(redirectUri);
  const html = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Authorize MCP echo</title>
    <style>
      body { font: 16px/1.5 system-ui, sans-serif; max-width: 36rem; margin: 10vh auto; padding: 1.5rem; }
      main { border: 1px solid #d1d5db; border-radius: 12px; padding: 1.5rem; }
      .actions { display: flex; gap: .75rem; margin-top: 1.5rem; }
      button { padding: .65rem 1rem; border: 0; border-radius: 8px; cursor: pointer; }
      button[name="decision"][value="approve"] { background: #111827; color: white; }
    </style>
  </head>
  <body>
    <main>
      <h1>Authorize MCP echo</h1>
      <p><strong>${escapeHtml(clientName ?? "An MCP client")}</strong> wants to use the echo tool as Demo User.</p>
      <p>Scopes to grant: <code>${requestedScopes}</code></p>
      <form method="post" action="${action}">
        <div class="actions">
          <button type="submit" name="decision" value="approve">Approve</button>
          <button type="submit" name="decision" value="deny">Deny</button>
        </div>
      </form>
    </main>
  </body>
</html>`;

  return new Response(html, {
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store",
      "Content-Security-Policy": `default-src 'none'; style-src 'unsafe-inline'; form-action 'self' ${redirectOrigin}; base-uri 'none'; frame-ancestors 'none'`,
      "X-Content-Type-Options": "nosniff",
    },
  });
}

function cspOrigin(redirectUri: string): string {
  const url = new URL(redirectUri);
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("OAuth redirect URI must use HTTP or HTTPS");
  }
  return url.origin;
}

function echoScopes(requestedScopes: string[]): string[] {
  if (requestedScopes.length === 0) return [ECHO_SCOPE];
  return requestedScopes.filter((scope) => scope === ECHO_SCOPE);
}

function escapeHtml(value: string): string {
  return value.replace(
    /[&<>"']/g,
    (character) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!,
  );
}

const oauthProvider = new OAuthProvider<Env>({
  apiRoute: "/mcp",
  apiHandler: EchoMcpHandler,
  defaultHandler,
  authorizeEndpoint: "/authorize",
  tokenEndpoint: "/oauth/token",
  scopesSupported: [ECHO_SCOPE],
  clientIdMetadataDocumentEnabled: true,
  clientRegistrationEndpoint: "/oauth/register",
});

function publicRequest(request: Request): Request {
  const forwardedProtocol = request.headers
    .get("X-Forwarded-Proto")
    ?.split(",", 1)[0]
    ?.trim()
    .toLowerCase();
  const url = new URL(request.url);

  if (forwardedProtocol !== "https" || url.protocol === "https:") return request;

  url.protocol = "https:";
  const init: RequestInit = {
    method: request.method,
    headers: request.headers,
    redirect: "manual",
  };
  if (request.method !== "GET" && request.method !== "HEAD") init.body = request.body;
  return new Request(url, init);
}

export default {
  fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    return oauthProvider.fetch(publicRequest(request), env, ctx);
  },
};
