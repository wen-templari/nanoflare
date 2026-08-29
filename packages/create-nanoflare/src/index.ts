import { cp, lstat, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { basename, dirname, isAbsolute, relative, resolve } from "node:path";
import process from "node:process";
import { createInterface } from "node:readline/promises";
import { fileURLToPath } from "node:url";

const packageDirectory = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const templatesDirectory = resolve(packageDirectory, "templates");

type Template = { id: string; description: string; directory: string };
type ParsedArgs = {
  directory?: string;
  template?: string;
  overwrite: boolean;
  interactive?: boolean;
  help: boolean;
};
type Output = { write(value: string): unknown };
type RunOptions = {
  cwd?: string;
  interactive?: boolean;
  now?: Date;
  prompt?: (message: string) => Promise<string>;
  stdout?: Output;
};

export const templates: Template[] = [
  { id: "starter", description: "A minimal TypeScript Worker", directory: "starter" },
  {
    id: "bindings",
    description: "A Hono Worker with KV and object storage",
    directory: "bindings",
  },
  { id: "pages", description: "A Vite and Tailwind static site", directory: "pages" },
  { id: "spa", description: "A React SPA with a Worker API route", directory: "spa" },
  { id: "ssr", description: "A React SSR app with Hono", directory: "ssr" },
  { id: "api", description: "A documented Hono and Drizzle API", directory: "api" },
  { id: "mcp", description: "A public MCP Worker", directory: "mcp" },
  { id: "oauth-mcp", description: "An OAuth-protected MCP Worker", directory: "oauth-mcp" },
];

export const helpMessage = `Usage: create-nanoflare [OPTION]... [DIRECTORY]

Create a new Nanoflare Worker. When running in a terminal, the CLI starts in interactive mode.

Options:
  -t, --template NAME                 use a specific template
  --overwrite                         remove existing files if target directory is not empty
  --interactive / --no-interactive    force interactive or non-interactive mode
  -h, --help                          display this help message

Available templates:
  starter                             A minimal TypeScript Worker
  bindings                            A Hono Worker with KV and object storage
  pages                               A Vite and Tailwind static site
  spa                                 A React SPA with a Worker API route
  ssr                                 A React SSR app with Hono
  api                                 A documented Hono and Drizzle API
  mcp                                 A public MCP Worker
  oauth-mcp                           An OAuth-protected MCP Worker
`;

export function parseArgs(args: string[]): ParsedArgs {
  const result: ParsedArgs = { overwrite: false, help: false };
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === "--") {
      for (const value of args.slice(index + 1)) addDirectory(result, value);
      break;
    }
    if (arg === "--help" || arg === "-h") result.help = true;
    else if (arg === "--overwrite") result.overwrite = true;
    else if (arg === "--interactive") result.interactive = true;
    else if (arg === "--no-interactive") result.interactive = false;
    else if (arg === "--template" || arg === "-t") {
      result.template = args[++index];
      if (!result.template) throw new Error(`${arg} requires a template name`);
    } else if (arg.startsWith("--template=")) {
      result.template = arg.slice("--template=".length);
      if (!result.template) throw new Error("--template requires a template name");
    } else if (arg.startsWith("-")) throw new Error(`Unknown option ${arg}`);
    else addDirectory(result, arg);
  }
  return result;
}

function addDirectory(result: ParsedArgs, directory: string): void {
  if (result.directory !== undefined)
    throw new Error("Usage: create-nanoflare [OPTION]... [DIRECTORY]");
  result.directory = directory;
}

function findTemplate(id: string): Template {
  const template = templates.find((entry) => entry.id === id);
  if (!template)
    throw new Error(
      `Unknown template ${JSON.stringify(id)} (available: ${templates.map((entry) => entry.id).join(", ")})`,
    );
  return template;
}

function isInteractiveTerminal(): boolean {
  return Boolean(process.stdin.isTTY && process.stdout.isTTY);
}

async function promptWithReadline(message: string): Promise<string> {
  const readline = createInterface({ input: process.stdin, output: process.stdout });
  try {
    return await readline.question(message);
  } finally {
    readline.close();
  }
}

function isMissingPath(error: unknown): boolean {
  return typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT";
}

async function directoryIsEmpty(path: string): Promise<boolean> {
  try {
    const info = await lstat(path);
    if (!info.isDirectory()) throw new Error(`Target ${path} is not a directory`);
    return (await readdir(path)).length === 0;
  } catch (error) {
    if (isMissingPath(error)) return true;
    throw error;
  }
}

async function emptyDirectory(path: string): Promise<void> {
  for (const entry of await readdir(path))
    await rm(resolve(path, entry), { recursive: true, force: true });
}

function projectName(directory: string): string {
  const name = basename(directory);
  if (!name || name === "." || name === "/")
    throw new Error("Choose a project directory with a name");
  return name;
}

function compatibilityDate(now: Date): string {
  return now.toISOString().slice(0, 10);
}

function destinationInside(path: string, root: string): boolean {
  const pathRelativeToRoot = relative(root, path);
  return (
    Boolean(pathRelativeToRoot) &&
    !pathRelativeToRoot.startsWith("..") &&
    !isAbsolute(pathRelativeToRoot)
  );
}

async function writeTemplate(
  template: Template,
  destination: string,
  name: string,
  now: Date,
): Promise<void> {
  const source = resolve(templatesDirectory, template.directory);
  if (!destinationInside(source, templatesDirectory)) throw new Error("Invalid template path");
  for (const entry of await readdir(source)) {
    await cp(resolve(source, entry), resolve(destination, entry), {
      recursive: true,
      errorOnExist: true,
      force: false,
    });
  }
  const projectPath = resolve(destination, "nanoflare.json");
  const project = await readFile(projectPath, "utf8");
  await writeFile(
    projectPath,
    project
      .replaceAll("{{projectName}}", name)
      .replaceAll("{{compatibilityDate}}", compatibilityDate(now)),
  );
}

export async function run(
  args: string[],
  options: RunOptions = {},
): Promise<{ status: "help" } | { status: "created"; destination: string; template: string }> {
  const parsed = parseArgs(args);
  const stdout = options.stdout ?? process.stdout;
  const prompt = options.prompt ?? promptWithReadline;
  const interactive = parsed.interactive ?? options.interactive ?? isInteractiveTerminal();
  const cwd = options.cwd ?? process.cwd();
  const now = options.now ?? new Date();
  if (parsed.help) {
    stdout.write(helpMessage);
    return { status: "help" };
  }

  let directory = parsed.directory ?? "nanoflare-worker";
  if (interactive && parsed.directory === undefined) {
    const response = (await prompt(`Project name [${directory}]: `)).trim();
    if (response) directory = response;
  }
  const destination = resolve(cwd, directory);
  const name = projectName(destination);
  let template = parsed.template ? findTemplate(parsed.template) : undefined;
  if (!template && interactive) {
    const response = (await prompt(`Select a template [starter]: `)).trim();
    template = findTemplate(response || "starter");
  }
  template ??= templates[0];

  if (!(await directoryIsEmpty(destination))) {
    let overwrite = parsed.overwrite;
    if (!overwrite && interactive) {
      const answer = (
        await prompt(
          `Target directory ${directory} is not empty. Remove existing files and continue? [y/N] `,
        )
      )
        .trim()
        .toLowerCase();
      overwrite = answer === "y" || answer === "yes";
    }
    if (!overwrite)
      throw new Error(
        `Target directory ${destination} is not empty. Use --overwrite to remove its contents.`,
      );
    await emptyDirectory(destination);
  }

  await mkdir(destination, { recursive: true });
  await writeTemplate(template, destination, name, now);
  stdout.write(
    `\nScaffolded a Nanoflare Worker in ${destination}\n\nNext steps:\n  cd ${directory}\n  nanoflare create\n  nanoflare deploy\n`,
  );
  return { status: "created", destination, template: template.id };
}

export async function main(args: string[]): Promise<void> {
  try {
    await run(args);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`create-nanoflare: ${message}\n`);
    process.exitCode = 1;
  }
}

export async function readStarterTemplate(): Promise<string> {
  return readFile(resolve(templatesDirectory, "starter", "worker.ts"), "utf8");
}
