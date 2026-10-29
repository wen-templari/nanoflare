package config

import (
	"strings"

	"github.com/clas/nanoflare/internal/dnsresolver"
)

// workerd's native node:dns sends JSON DoH through the current global fetch.
// Install before user modules execute, including modules that capture fetch.
func dohFetchSource() string {
	return strings.NewReplacer("__DOH_HOST__", dnsresolver.DoHHost, "__DOH_PATH__", dnsresolver.DoHPath).Replace(`import { env } from "cloudflare:workers";
const nativeFetch = globalThis.fetch;
globalThis.fetch = async function fetch(input, init) {
  const value = typeof input === "string" ? input : input instanceof URL ? input.href : input?.url;
  if (typeof value === "string") {
    let url;
    try { url = new URL(value); } catch {}
    if (url?.protocol === "https:" && url.hostname.replace(/\.$/, "").toLowerCase() === "__DOH_HOST__" &&
        !url.port && !url.username && !url.password && url.pathname === "__DOH_PATH__" && url.searchParams.has("name")) {
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (String(method).toUpperCase() === "GET" && env.__NANOFLARE_DNS) {
        const request = new Request(input, init);
        request.signal.throwIfAborted();
        return env.__NANOFLARE_DNS.fetch("http://nanoflare.dns__DOH_PATH__" + url.search, {
          headers: { accept: "application/dns-json" },
          signal: request.signal,
        });
      }
    }
  }
  return nativeFetch.call(this, input, init);
};
`)
}
