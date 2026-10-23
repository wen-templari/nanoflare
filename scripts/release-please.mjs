import { appendFileSync } from "node:fs";
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { pathToFileURL } from "node:url";

// Work around googleapis/release-please#2905 until upstream retries these errors.
// Retry only read-only GraphQL requests, never release or PR mutations.
export function retryGraphqlFetch(fetchImpl, { sleep = delay, warn = console.warn } = {}) {
  return async (url, options) => {
    let query;
    try {
      query = JSON.parse(options?.body).query;
    } catch {
      // REST requests and requests without a JSON body pass through unchanged.
    }
    const readOnly =
      new URL(url).pathname.endsWith("/graphql") &&
      typeof query === "string" &&
      /^\s*query\b/.test(query);
    if (!readOnly) return fetchImpl(url, options);

    for (let attempt = 0; ; attempt++) {
      const response = await fetchImpl(url, options);
      let errors;
      try {
        errors = (await response.clone().json()).errors;
      } catch {
        // A gateway failure may return HTML instead of JSON.
      }
      const transientStatus = [500, 502, 503, 504].includes(response.status);
      const transientErrors =
        response.status === 200 &&
        Array.isArray(errors) &&
        errors.length > 0 &&
        errors.every(({ message = "" }) =>
          /something went wrong while executing your query|we couldn't respond to your request in time/i.test(
            message,
          ),
        );
      if ((!transientStatus && !transientErrors) || attempt === 3) return response;
      const waitMs = 5_000 * 2 ** attempt;
      warn(`GitHub history query failed; retry ${attempt + 1}/3 in ${waitMs / 1000}s`);
      await sleep(waitMs);
    }
  };
}

export async function main() {
  const require = createRequire(resolve(process.env.RELEASE_PLEASE_RUNTIME, "package.json"));
  const { GitHub, Manifest } = require("release-please");
  const [owner, repo] = process.env.GITHUB_REPOSITORY.split("/");
  const github = await GitHub.create({
    owner,
    repo,
    token: process.env.GH_TOKEN,
    defaultBranch: "main",
    fetch: retryGraphqlFetch(globalThis.fetch),
  });
  const loadManifest = () =>
    Manifest.fromManifest(
      github,
      "main",
      "release-please-config.json",
      ".release-please-manifest.json",
    );
  await (await loadManifest()).createReleases();
  const prs = await (await loadManifest()).createPullRequests();
  appendFileSync(process.env.GITHUB_OUTPUT, `prs_created=${prs.some(Boolean)}\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await main();
}
