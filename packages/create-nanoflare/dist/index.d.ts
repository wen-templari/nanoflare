type Template = {
  id: string;
  description: string;
  directory: string;
};
type ParsedArgs = {
  directory?: string;
  template?: string;
  overwrite: boolean;
  interactive?: boolean;
  help: boolean;
};
type Output = {
  write(value: string): unknown;
};
type RunOptions = {
  cwd?: string;
  interactive?: boolean;
  now?: Date;
  prompt?: (message: string) => Promise<string>;
  stdout?: Output;
};
export declare const templates: Template[];
export declare const helpMessage =
  "Usage: create-nanoflare [OPTION]... [DIRECTORY]\n\nCreate a new Nanoflare Worker. When running in a terminal, the CLI starts in interactive mode.\n\nOptions:\n  -t, --template NAME                 use a specific template\n  --overwrite                         remove existing files if target directory is not empty\n  --interactive / --no-interactive    force interactive or non-interactive mode\n  -h, --help                          display this help message\n\nAvailable templates:\n  starter                             A minimal TypeScript Worker\n  bindings                            A Hono Worker with KV and object storage\n  pages                               A Vite and Tailwind static site\n  spa                                 A React SPA with a Worker API route\n  ssr                                 A React SSR app with Hono\n  api                                 A documented Hono and Drizzle API\n  mcp                                 A public MCP Worker\n  oauth-mcp                           An OAuth-protected MCP Worker\n";
export declare function parseArgs(args: string[]): ParsedArgs;
export declare function run(
  args: string[],
  options?: RunOptions,
): Promise<
  | {
      status: "help";
    }
  | {
      status: "created";
      destination: string;
      template: string;
    }
>;
export declare function main(args: string[]): Promise<void>;
export declare function readStarterTemplate(): Promise<string>;
