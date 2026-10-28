package config

// workerd's native node:dns resolves A/AAAA through the current global fetch.
// Install before user modules execute, including modules that capture fetch.
func dohFetchSource() string {
	return `import { env } from "cloudflare:workers";
const nativeFetch = globalThis.fetch;
globalThis.fetch = async function fetch(input, init) {
  const value = typeof input === "string" ? input : input instanceof URL ? input.href : input?.url;
  if (typeof value === "string") {
    let url;
    try { url = new URL(value); } catch {}
    if (url?.protocol === "https:" && url.hostname === "cloudflare-dns.com" &&
        !url.port && !url.username && !url.password && url.pathname === "/dns-query") {
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (String(method).toUpperCase() === "GET") {
        const request = new Request(input, init);
        request.signal.throwIfAborted();
        return env.__NANOFLARE_DNS.fetch("http://nanoflare.dns/dns-query" + url.search, {
          headers: { accept: "application/dns-json" },
          signal: request.signal,
        });
      }
    }
  }
  return nativeFetch.call(this, input, init);
};
`
}
