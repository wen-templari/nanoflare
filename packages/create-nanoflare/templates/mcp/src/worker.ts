import { createMcpHandler, McpServer } from "@modelcontextprotocol/server";
import * as z from "zod/v4";

function createServer(): McpServer {
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
    }),
  );

  return server;
}

const mcpHandler = createMcpHandler(createServer);

export default {
  fetch(request: Request): Promise<Response> | Response {
    const url = new URL(request.url);
    if (url.pathname === "/") {
      return Response.json({
        name: "Nanoflare MCP echo server",
        mcp_endpoint: new URL("/mcp", url).toString(),
      });
    }
    if (url.pathname === "/mcp") return mcpHandler.fetch(request);
    return new Response("Not found", { status: 404 });
  },
};
