import { WorkerEntrypoint } from "cloudflare:workers";

export interface User {
  id: string;
  email: string;
  role: "admin" | "member";
}

export default class IdentityWorker extends WorkerEntrypoint {
  getUser(id: string): User {
    return {
      id,
      email: `${id}@example.test`,
      role: id === "ada" ? "admin" : "member",
    };
  }

  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const id = url.searchParams.get("user") || "guest";
    return Response.json(this.getUser(id));
  }
}
