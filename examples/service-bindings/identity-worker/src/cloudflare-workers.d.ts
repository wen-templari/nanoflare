declare module "cloudflare:workers" {
  export class WorkerEntrypoint<Env = unknown> {
    readonly env: Env;
  }
}
