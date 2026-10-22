// Trusted probe shim. Counters and synthetic effects live in the relay process.
import { WorkerEntrypoint } from "cloudflare:workers";
export class Broker extends WorkerEntrypoint {
  async fetch(request) {
    return this.env.COUNTER.fetch("http://relay/budget/" + this.ctx.props.lease, {
      method: "POST",
      redirect: "manual",
      body: JSON.stringify({ url: request.url, kind: request.headers.get("x-poc-kind") || "http" }),
    });
  }
  async identify() {
    const response = await this.fetch(
      new Request("http://effect/rpc", { headers: { "x-poc-kind": "rpc" } }),
    );
    if (!response.ok) throw new Error("budget exceeded");
    return response.status;
  }
}
