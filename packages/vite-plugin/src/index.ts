import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin, ResolvedConfig, ViteDevServer } from "vite";

import { Miniflare } from "miniflare";
import { readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { extname, join, resolve } from "node:path";
import { Readable } from "node:stream";
import { build, normalizePath } from "vite";

export type BindingValue = string | number | boolean;

export interface NanoflarePluginOptions {
  /**
   * The Worker module Vite bundles and runs locally. When omitted, this comes
   * from `main` in `nanoflare.json`.
   */
  entry?: string;
  /**
   * The Nanoflare project file to load. Defaults to `nanoflare.json` in the
   * Vite root. Set to `false` to require all Worker settings in Vite config.
   */
  configPath?: string | false;
  /** Values exposed to the Worker as `env`. Explicit bindings override `.dev.vars`. */
  bindings?: Record<string, BindingValue>;
  /** A dotenv-style file containing local string bindings. Defaults to `.dev.vars`. */
  devVars?: string | false;
  /** Names of host environment variables to expose to the Worker. */
  env?: string[];
  /** The Workers compatibility date passed to the local runtime. */
  compatibilityDate?: string;
  /** Local D1 bindings, optionally persisted to a directory or enabled with `true`. */
  d1?: { bindings?: string[]; persist?: boolean | string };
  /** Local R2 bindings, optionally persisted to a directory or enabled with `true`. */
  r2?: { bindings?: string[]; persist?: boolean | string };
  /** Select which Vite requests should execute in the Worker. */
  include?: (request: IncomingMessage) => boolean;
}

interface NanoflareProjectConfig {
  main?: string;
  vite?: { entry?: string };
  compatibility_date?: string;
  vars?: Record<string, BindingValue>;
  db?: Array<{ binding?: string }>;
  object_storage_buckets?: Array<{ binding?: string }>;
}

interface ResolvedNanoflarePluginOptions extends NanoflarePluginOptions {
  entry: string;
}

const workerFileExtensions = new Set([".ts", ".tsx", ".js", ".jsx", ".mts", ".mjs"]);
const vitePathPrefixes = ["/@vite/", "/@id/", "/@fs/", "/node_modules/", "/__vite"];
const defaultCompatibilityDate = "2025-12-10";

/**
 * Runs one SSR/API Worker in Miniflare while Vite continues to serve client
 * modules, static files, and browser HMR. Server source changes rebuild the
 * Worker before the next matching request.
 */
export function nanoflare(options: NanoflarePluginOptions): Plugin {
  let config: ResolvedConfig;
  let resolvedOptions: ResolvedNanoflarePluginOptions;
  let server: ViteDevServer | undefined;
  let runtime: Miniflare | undefined;
  let outputDirectory: string | undefined;
  let stale = true;
  let rebuilding: Promise<void> | undefined;

  return {
    name: "nanoflare:vite-plugin",
    enforce: "post",
    config(userConfig) {
      const root = resolve(userConfig.root ?? process.cwd());
      const ignored = userConfig.server?.watch?.ignored;
      const persistencePaths = getPersistencePaths(options, root);

      if (!persistencePaths.length) return;

      return {
        server: {
          watch: {
            ignored: [
              ...(ignored === undefined ? [] : Array.isArray(ignored) ? ignored : [ignored]),
              ...persistencePaths.map((path) => `${path}/**`),
            ],
          },
        },
      };
    },
    configResolved(resolvedConfig) {
      config = resolvedConfig;
      return resolvePluginOptions(options, config.root).then((nextOptions) => {
        resolvedOptions = nextOptions;
      });
    },
    async configureServer(viteServer) {
      server = viteServer;
      outputDirectory = await createTemporaryDirectory();

      viteServer.watcher.on("change", (changedPath) => {
        if (shouldRebuildWorker(changedPath, config.root)) {
          stale = true;
        }
      });

      const close = viteServer.close.bind(viteServer);
      viteServer.close = async () => {
        try {
          await runtime?.dispose();
          if (outputDirectory) await rm(outputDirectory, { force: true, recursive: true });
        } finally {
          await close();
        }
      };

      viteServer.middlewares.use(async (request, response, next) => {
        if (!shouldHandleRequest(request, resolvedOptions.include)) {
          next();
          return;
        }

        try {
          await ensureRuntime();
          const [workerRequest, workerRequestInit] = await toWorkerRequest(request, viteServer);
          const workerResponse = await runtime?.dispatchFetch(
            workerRequest,
            workerRequestInit as never,
          );

          if (!workerResponse) {
            throw new Error("Nanoflare local Worker is not available.");
          }

          await writeWorkerResponse(workerResponse as unknown as WorkerResponse, response);
        } catch (error) {
          viteServer.config.logger.error(
            `[nanoflare] Worker request failed: ${formatError(error)}`,
          );
          if (!response.headersSent) {
            response.statusCode = 500;
            response.setHeader("content-type", "text/plain; charset=utf-8");
            response.end("Nanoflare Worker request failed. See the Vite terminal for details.");
          }
        }
      });
    },
  };

  async function ensureRuntime(): Promise<void> {
    if (!stale && runtime) return;
    if (rebuilding) return rebuilding;

    rebuilding = rebuildRuntime().finally(() => {
      rebuilding = undefined;
    });
    return rebuilding;
  }

  async function rebuildRuntime(): Promise<void> {
    if (!server || !outputDirectory) {
      throw new Error("Nanoflare Vite plugin was not initialized.");
    }

    await build({
      root: config.root,
      configFile: false,
      appType: "custom",
      resolve: { alias: config.resolve.alias },
      define: config.define,
      esbuild: config.esbuild,
      ssr: { noExternal: true },
      logLevel: "error",
      build: {
        ssr: resolve(config.root, resolvedOptions.entry),
        outDir: outputDirectory,
        emptyOutDir: false,
        sourcemap: true,
        minify: false,
        target: "es2022",
        rollupOptions: {
          output: {
            entryFileNames: "worker.mjs",
            chunkFileNames: "chunks/[name]-[hash].mjs",
            assetFileNames: "assets/[name]-[hash][extname]",
          },
        },
      },
    });

    const nextRuntime = new Miniflare({
      modules: true,
      modulesRoot: outputDirectory,
      scriptPath: join(outputDirectory, "worker.mjs"),
      compatibilityDate: resolvedOptions.compatibilityDate ?? defaultCompatibilityDate,
      bindings: await getBindings(resolvedOptions, config.root),
      d1Databases: resolvedOptions.d1?.bindings,
      d1Persist: resolvePersistencePath(resolvedOptions.d1?.persist, config.root),
      r2Buckets: resolvedOptions.r2?.bindings,
      r2Persist: resolvePersistencePath(resolvedOptions.r2?.persist, config.root),
    });

    await nextRuntime.ready;
    const previousRuntime = runtime;
    runtime = nextRuntime;
    stale = false;
    await previousRuntime?.dispose();
  }
}

async function resolvePluginOptions(
  options: NanoflarePluginOptions,
  root: string,
): Promise<ResolvedNanoflarePluginOptions> {
  const project = await readProjectConfig(options.configPath, root);
  const entry = options.entry ?? project?.vite?.entry ?? project?.main;
  if (!entry) {
    throw new Error(
      "[nanoflare] Set `main` or `vite.entry` in nanoflare.json, or pass the plugin `entry` option.",
    );
  }

  return {
    ...options,
    entry,
    compatibilityDate: options.compatibilityDate ?? project?.compatibility_date,
    bindings: { ...project?.vars, ...options.bindings },
    d1: mergeLocalBindingOptions(getD1Options(project), options.d1),
    r2: mergeLocalBindingOptions(getR2Options(project), options.r2),
  };
}

async function readProjectConfig(
  configPath: NanoflarePluginOptions["configPath"],
  root: string,
): Promise<NanoflareProjectConfig | undefined> {
  if (configPath === false) return undefined;
  const path = resolve(root, configPath ?? "nanoflare.json");
  try {
    return JSON.parse(await readFile(path, "utf8")) as NanoflareProjectConfig;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT" && configPath === undefined) {
      return undefined;
    }
    throw new Error(`[nanoflare] Could not read ${path}: ${formatError(error)}`);
  }
}

function getD1Options(project: NanoflareProjectConfig | undefined) {
  const bindings = project?.db?.flatMap(({ binding }) => (binding ? [binding] : []));
  return bindings?.length ? { bindings } : undefined;
}

function getR2Options(project: NanoflareProjectConfig | undefined) {
  const bindings = project?.object_storage_buckets?.flatMap(({ binding }) =>
    binding ? [binding] : [],
  );
  return bindings?.length ? { bindings } : undefined;
}

function mergeLocalBindingOptions(
  projectOptions: NanoflarePluginOptions["d1"],
  viteOptions: NanoflarePluginOptions["d1"],
): NanoflarePluginOptions["d1"] {
  if (!projectOptions && !viteOptions) return undefined;
  return {
    ...projectOptions,
    ...viteOptions,
    bindings: viteOptions?.bindings ?? projectOptions?.bindings,
  };
}

function shouldHandleRequest(
  request: IncomingMessage,
  include: NanoflarePluginOptions["include"],
): boolean {
  if (include) return include(request);
  const url = new URL(request.url ?? "/", "http://vite.local");
  if (vitePathPrefixes.some((prefix) => url.pathname.startsWith(prefix))) return false;
  if (url.searchParams.has("import") || url.searchParams.has("raw") || url.searchParams.has("url"))
    return false;
  if (workerFileExtensions.has(extname(url.pathname))) return false;
  if (url.pathname.startsWith("/api/")) return true;
  return request.headers.accept?.includes("text/html") ?? false;
}

function shouldRebuildWorker(changedPath: string, root: string): boolean {
  const relativePath = normalizePath(changedPath).slice(normalizePath(root).length + 1);
  return (
    !relativePath.startsWith("node_modules/") && workerFileExtensions.has(extname(changedPath))
  );
}

async function getBindings(
  options: NanoflarePluginOptions,
  root: string,
): Promise<Record<string, BindingValue>> {
  const devVars =
    options.devVars === false
      ? {}
      : await readDevVars(resolve(root, options.devVars ?? ".dev.vars"));
  const processBindings = Object.fromEntries(
    (options.env ?? []).flatMap((name) => {
      const value = process.env[name];
      return value === undefined ? [] : [[name, value]];
    }),
  );
  return { ...devVars, ...processBindings, ...options.bindings };
}

function resolvePersistencePath(
  persist: boolean | string | undefined,
  root: string,
): boolean | string | undefined {
  return typeof persist === "string" ? resolve(root, persist) : persist;
}

function getPersistencePaths(options: NanoflarePluginOptions, root: string): string[] {
  return [options.d1?.persist, options.r2?.persist].flatMap((persist) =>
    typeof persist === "string" ? [resolve(root, persist)] : [],
  );
}

async function readDevVars(path: string): Promise<Record<string, string>> {
  try {
    const contents = await readFile(path, "utf8");
    return Object.fromEntries(
      contents.split(/\r?\n/).flatMap((line) => {
        const match = line.match(/^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$/);
        if (!match || line.trimStart().startsWith("#")) return [];
        const [, name, rawValue] = match;
        const value = rawValue.replace(/^(?:"([\s\S]*)"|'([\s\S]*)')$/, "$1$2");
        return [[name, value]];
      }),
    );
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return {};
    throw error;
  }
}

async function toWorkerRequest(
  request: IncomingMessage,
  server: ViteDevServer,
): Promise<[URL, RequestInit]> {
  const protocol = server.config.server.https ? "https" : "http";
  const host = request.headers.host ?? "localhost";
  const headers = new Headers();
  for (const [name, value] of Object.entries(request.headers)) {
    if (value === undefined || name === "host") continue;
    headers.set(name, Array.isArray(value) ? value.join(", ") : String(value));
  }

  const method = request.method ?? "GET";
  const init: RequestInit & { duplex?: "half" } = { method, headers };
  if (method !== "GET" && method !== "HEAD") {
    init.body = (await readRequestBody(request)) as unknown as BodyInit;
  }
  return [new URL(request.url ?? "/", `${protocol}://${host}`), init];
}

async function readRequestBody(request: IncomingMessage): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  for await (const chunk of request) {
    chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : chunk);
  }
  return Buffer.concat(chunks);
}

interface WorkerResponse {
  status: number;
  headers: Iterable<[string, string]> & { getSetCookie?: () => string[] };
  body: ReadableStream<Uint8Array> | null;
}

async function writeWorkerResponse(
  response: WorkerResponse,
  target: ServerResponse,
): Promise<void> {
  target.statusCode = response.status;
  for (const [name, value] of response.headers) target.setHeader(name, value);
  const setCookie = (
    response.headers as Headers & { getSetCookie?: () => string[] }
  ).getSetCookie?.();
  if (setCookie?.length) target.setHeader("set-cookie", setCookie);
  if (!response.body) {
    target.end();
    return;
  }
  await new Promise<void>((resolve, reject) => {
    Readable.fromWeb(response.body as import("node:stream/web").ReadableStream)
      .on("error", reject)
      .pipe(target)
      .on("finish", resolve)
      .on("error", reject);
  });
}

async function createTemporaryDirectory(): Promise<string> {
  const { mkdtemp } = await import("node:fs/promises");
  return mkdtemp(join(tmpdir(), "nanoflare-vite-"));
}

function formatError(error: unknown): string {
  return error instanceof Error ? (error.stack ?? error.message) : String(error);
}
