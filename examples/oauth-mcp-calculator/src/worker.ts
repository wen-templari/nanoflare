import {
  AuthorizationError,
  OAuthProvider,
  type AuthRequest,
  type OAuthHelpers,
} from "@cloudflare/workers-oauth-provider";
import { createMcpHandler, McpServer } from "@modelcontextprotocol/server";
import { WorkerEntrypoint } from "cloudflare:workers";
import * as z from "zod/v4";

const CALCULATOR_SCOPE = "calculator:use";

interface AuthProps {
  userId: string;
  displayName: string;
  scopes: string[];
}

interface Env {
  OAUTH_KV: KVNamespace;
  OAUTH_PROVIDER: OAuthHelpers;
}

function calculatorServer(user: AuthProps, debugId: string | null): McpServer {
  const server = new McpServer(
    { name: "nanoflare-calculator", version: "1.0.0" },
    { capabilities: { tools: {} } },
  );

  server.registerTool(
    "add",
    {
      description: "Add two numbers.",
      inputSchema: z.object({
        a: z.number().describe("First number"),
        b: z.number().describe("Second number"),
      }),
    },
    async ({ a, b }) => {
      const startedAt = Date.now();
      console.info(
        "[oauth-debug]",
        JSON.stringify({
          event: "tool.call_started",
          debugId,
          tool: "add",
          arguments: { a, b },
        }),
      );

      const result = a + b;
      console.info(
        "[oauth-debug]",
        JSON.stringify({
          event: "tool.call_completed",
          debugId,
          tool: "add",
          durationMs: Date.now() - startedAt,
          result,
        }),
      );

      return {
        content: [
          {
            type: "text" as const,
            text: `${result}`,
          },
        ],
        _meta: {
          user: user.displayName,
        },
      };
    },
  );

  return server;
}

export class CalculatorMcpHandler extends WorkerEntrypoint<Env, AuthProps> {
  async fetch(request: Request): Promise<Response> {
    oauthDebug(request, "mcp.request", {
      mcpMethod: request.headers.get("mcp-method"),
      protocolVersion: request.headers.get("mcp-protocol-version"),
      grantedScopes: this.ctx.props.scopes,
    });

    if (!this.ctx.props.scopes.includes(CALCULATOR_SCOPE)) {
      oauthDebug(request, "mcp.scope_denied", {
        requiredScope: CALCULATOR_SCOPE,
        grantedScopes: this.ctx.props.scopes,
      });
      return new Response("The access token does not grant calculator:use.", { status: 403 });
    }

    const debugId = request.headers.get("X-OAuth-Debug-Id");
    const handler = createMcpHandler(() => calculatorServer(this.ctx.props, debugId));
    return handler.fetch(request);
  }
}

const defaultHandler: ExportedHandler<Env> = {
  async fetch(request, env) {
    const url = new URL(request.url);

    if (url.pathname === "/") {
      return Response.json({
        name: "Nanoflare OAuth MCP calculator",
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
      oauthDebug(request, "authorize.parse_failed", safeError(error));
      return authorizationErrorResponse(error);
    }

    oauthDebug(request, "authorize.parsed", {
      clientId: redactIdentifier(oauthRequest.clientId),
      redirectUri: oauthRequest.redirectUri,
      requestedScopes: oauthRequest.scope,
      hasState: Boolean(oauthRequest.state),
      issuer: oauthRequest.issuer,
    });

    const client = await env.OAUTH_PROVIDER.lookupClient(oauthRequest.clientId);
    if (!client) {
      oauthDebug(request, "authorize.unknown_client", {
        clientId: redactIdentifier(oauthRequest.clientId),
      });
      return new Response("Unknown OAuth client", { status: 400 });
    }

    const scopes = calculatorScopes(oauthRequest.scope);
    if (request.method === "GET") {
      oauthDebug(request, "authorize.consent_rendered", {
        clientName: client.clientName,
        grantedScopes: scopes,
      });
      return consentPage(url, client.clientName, scopes, oauthRequest.redirectUri);
    }

    if (request.method !== "POST") {
      return new Response("Method not allowed", {
        status: 405,
        headers: { Allow: "GET, POST" },
      });
    }

    const form = await request.formData();
    const decision = form.get("decision") === "approve" ? "approve" : "deny";
    oauthDebug(request, "authorize.consent_submitted", { decision });
    if (decision !== "approve") {
      const redirect = new URL(oauthRequest.redirectUri);
      redirect.searchParams.set("error", "access_denied");
      redirect.searchParams.set("error_description", "The user denied the request.");
      if (oauthRequest.state) redirect.searchParams.set("state", oauthRequest.state);
      if (oauthRequest.issuer) redirect.searchParams.set("iss", oauthRequest.issuer);
      oauthDebug(request, "authorize.denied", {
        redirect: safeRedirect(redirect.toString()),
      });
      return Response.redirect(redirect, 302);
    }

    const user = { id: "demo-user", displayName: "Demo User" };
    let redirectTo: string;
    try {
      ({ redirectTo } = await env.OAUTH_PROVIDER.completeAuthorization({
        request: oauthRequest,
        userId: user.id,
        metadata: { clientName: client.clientName },
        scope: scopes,
        props: {
          userId: user.id,
          displayName: user.displayName,
          scopes,
        },
      }));
    } catch (error) {
      oauthDebug(request, "authorize.completion_failed", safeError(error));
      throw error;
    }

    oauthDebug(request, "authorize.completed", {
      redirect: safeRedirect(redirectTo),
      grantedScopes: scopes,
    });

    return Response.redirect(redirectTo, 302);
  },
};

function authorizationErrorResponse(error: unknown): Response {
  if (!(error instanceof AuthorizationError)) throw error;
  if (!error.redirectUri) {
    return new Response(error.description, { status: 400 });
  }

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
    <title>Authorize calculator</title>
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
      <h1>Authorize calculator</h1>
      <p><strong>${escapeHtml(clientName ?? "An MCP client")}</strong> wants to use the calculator as Demo User.</p>
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
      // The form posts to this Worker, then OAuth redirects to the registered
      // client callback. Browsers apply form-action to that redirect chain, so
      // the callback origin must be allowed as well as 'self'.
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

function calculatorScopes(requestedScopes: string[]): string[] {
  if (requestedScopes.length === 0) {
    return [CALCULATOR_SCOPE];
  }
  return requestedScopes.filter((scope) => scope === CALCULATOR_SCOPE);
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
  apiHandler: CalculatorMcpHandler,
  defaultHandler,
  authorizeEndpoint: "/authorize",
  tokenEndpoint: "/oauth/token",
  scopesSupported: [CALCULATOR_SCOPE],
  clientIdMetadataDocumentEnabled: true,
  clientRegistrationEndpoint: "/oauth/register",
});

const worker: ExportedHandler<Env> = {
  async fetch(request, env, ctx) {
    const startedAt = Date.now();
    const debugId = crypto.randomUUID();
    const publicFacingRequest = withDebugId(publicRequest(request), debugId);
    const url = new URL(publicFacingRequest.url);

    console.info(
      "[oauth-debug]",
      JSON.stringify({
        event: "request.started",
        debugId,
        method: publicFacingRequest.method,
        path: url.pathname,
        queryParameters: [...url.searchParams.keys()],
        hasAuthorization: publicFacingRequest.headers.has("Authorization"),
        mcpMethod: publicFacingRequest.headers.get("mcp-method"),
        protocolVersion: publicFacingRequest.headers.get("mcp-protocol-version"),
      }),
    );

    try {
      const response = await oauthProvider.fetch(publicFacingRequest, env, ctx as ExecutionContext);
      console.info(
        "[oauth-debug]",
        JSON.stringify({
          event: "request.completed",
          debugId,
          method: publicFacingRequest.method,
          path: url.pathname,
          status: response.status,
          durationMs: Date.now() - startedAt,
          redirect: safeRedirect(response.headers.get("Location")),
          hasBearerChallenge: response.headers.has("WWW-Authenticate"),
        }),
      );
      return withDebugResponseId(
        instrumentResponseBody(response, {
          debugId,
          method: publicFacingRequest.method,
          path: url.pathname,
          mcpMethod: publicFacingRequest.headers.get("mcp-method"),
          startedAt,
        }),
        debugId,
      );
    } catch (error) {
      console.error(
        "[oauth-debug]",
        JSON.stringify({
          event: "request.failed",
          debugId,
          method: publicFacingRequest.method,
          path: url.pathname,
          durationMs: Date.now() - startedAt,
          ...safeError(error),
        }),
      );
      throw error;
    }
  },
};

function oauthDebug(request: Request, event: string, details: Record<string, unknown> = {}): void {
  console.info(
    "[oauth-debug]",
    JSON.stringify({
      event,
      debugId: request.headers.get("X-OAuth-Debug-Id"),
      ...details,
    }),
  );
}

function withDebugId(request: Request, debugId: string): Request {
  const headers = new Headers(request.headers);
  headers.set("X-OAuth-Debug-Id", debugId);
  return new Request(request, { headers });
}

function withDebugResponseId(response: Response, debugId: string): Response {
  const headers = new Headers(response.headers);
  headers.set("X-OAuth-Debug-Id", debugId);
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

function instrumentResponseBody(
  response: Response,
  request: {
    debugId: string;
    method: string;
    path: string;
    mcpMethod: string | null;
    startedAt: number;
  },
): Response {
  if (!response.body) return response;

  let firstChunk = true;
  let bytesWritten = 0;
  const body = response.body.pipeThrough(
    new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, controller) {
        bytesWritten += chunk.byteLength;
        if (firstChunk) {
          firstChunk = false;
          console.info(
            "[oauth-debug]",
            JSON.stringify({
              event: "response.first_chunk",
              debugId: request.debugId,
              method: request.method,
              path: request.path,
              mcpMethod: request.mcpMethod,
              durationMs: Date.now() - request.startedAt,
              chunkBytes: chunk.byteLength,
            }),
          );
        }
        controller.enqueue(chunk);
      },
      flush() {
        console.info(
          "[oauth-debug]",
          JSON.stringify({
            event: "response.body_completed",
            debugId: request.debugId,
            method: request.method,
            path: request.path,
            mcpMethod: request.mcpMethod,
            durationMs: Date.now() - request.startedAt,
            bytesWritten,
          }),
        );
      },
    }),
  );

  return new Response(body, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}

function safeRedirect(location: string | null): string | null {
  if (!location) return null;
  try {
    const url = new URL(location);
    const parameterNames = [...new Set(url.searchParams.keys())].sort();
    return `${url.origin}${url.pathname}${parameterNames.length > 0 ? `?${parameterNames.join("&")}` : ""}`;
  } catch {
    return "invalid redirect URL";
  }
}

function redactIdentifier(value: string): string {
  return value.length <= 8 ? `${value.slice(0, 2)}…` : `${value.slice(0, 6)}…${value.slice(-2)}`;
}

function safeError(error: unknown): Record<string, unknown> {
  if (!(error instanceof Error)) {
    return { errorType: typeof error };
  }
  return {
    errorName: error.name,
    errorMessage: error.message,
  };
}

function publicRequest(request: Request): Request {
  const forwardedProtocol = request.headers
    .get("X-Forwarded-Proto")
    ?.split(",", 1)[0]
    ?.trim()
    .toLowerCase();
  const url = new URL(request.url);

  if (forwardedProtocol !== "https" || url.protocol === "https:") {
    return request;
  }

  url.protocol = "https:";
  // Keep OAuth callback responses visible to the browser instead of allowing
  // an internal fetch-shaped dispatch to follow them server-side.
  const init: RequestInit = {
    method: request.method,
    headers: request.headers,
    redirect: "manual",
  };
  if (request.method !== "GET" && request.method !== "HEAD") {
    init.body = request.body;
  }
  return new Request(url, init);
}

export default worker;
