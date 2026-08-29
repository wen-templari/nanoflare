interface ExecutionContext {
  readonly props: unknown;
  waitUntil(promise: Promise<unknown>): void;
  passThroughOnException(): void;
}

interface ExportedHandler<Environment = unknown> {
  fetch?: (
    request: Request,
    env: Environment,
    ctx: ExecutionContext,
  ) => Response | Promise<Response>;
}

declare module "cloudflare:workers" {
  export class WorkerEntrypoint<Environment = unknown, Properties = unknown> {
    readonly env: Environment;
    readonly ctx: ExecutionContext & { readonly props: Properties };
  }
}
