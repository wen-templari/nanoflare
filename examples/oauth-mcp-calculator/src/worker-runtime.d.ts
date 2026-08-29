type KVNamespace = NanoflareKVNamespace;

interface ExecutionContext extends NanoflareExecutionContext {
  readonly props: unknown;
  passThroughOnException(): void;
}

interface ExportedHandler<Environment = unknown> extends NanoflareWorkerHandler<Environment> {}

declare module "cloudflare:workers" {
  export class WorkerEntrypoint<Environment = unknown, Properties = unknown> {
    readonly env: Environment;
    readonly ctx: ExecutionContext & { readonly props: Properties };
  }
}
