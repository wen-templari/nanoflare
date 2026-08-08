import { WorkerEntrypoint } from "cloudflare:workers";

export default class IdentityWorker extends WorkerEntrypoint {
  getUser(id) {
    return {
      id,
      email: `${id}@example.test`,
      role: id === "ada" ? "admin" : "member",
    };
  }

  async fetch(request) {
    const url = new URL(request.url);
    const id = url.searchParams.get("user") || "guest";
    return Response.json(this.getUser(id));
  }
}
