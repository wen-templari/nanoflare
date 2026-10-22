// Synthetic HTTP effects only. Not a public API or production broker.
const http = require("node:http"),
  crypto = require("node:crypto");
const config = JSON.parse(process.argv[1]);
const cells = new Map(),
  leases = new Map(),
  history = [],
  denied = [];
const MAX_LEASES = 16,
  MAX_HISTORY = 64;
function json(res, status, value) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(value));
}
function archive(lease, reason) {
  if (!leases.delete(lease.id)) return;
  clearTimeout(lease.timer);
  lease.reason = reason;
  lease.finished = Date.now();
  history.push(lease);
  if (history.length > MAX_HISTORY) history.shift();
}
function snapshot(lease) {
  return {
    id: lease.id,
    path: lease.path,
    cell: lease.cell,
    revision: lease.revision,
    subject: lease.subject,
    state: lease.state,
    reason: lease.reason,
    accepted: lease.accepted,
    attempts: lease.attempts,
  };
}
http
  .createServer((req, res) => {
    if (req.url.startsWith("/__control/")) {
      if (req.headers.authorization !== "Bearer " + config.control) {
        json(res, 401, {});
        return;
      }
      const url = new URL(req.url, "http://fixture");
      if (url.pathname === "/__control/register") {
        const name = url.searchParams.get("cell");
        if (!name?.startsWith(config.prefix)) {
          json(res, 400, {});
          return;
        }
        cells.set(name, { revision: url.searchParams.get("revision") || "v1" });
        json(res, 200, {});
        return;
      }
      if (url.pathname === "/__control/revoke") {
        const lease = leases.get(url.searchParams.get("lease"));
        if (lease) {
          archive(lease, "explicit_revoke");
          lease.upstream?.destroy();
        }
        json(res, 200, {});
        return;
      }
      json(res, 200, {
        active: [...leases.values()].map(snapshot),
        history: history.map(snapshot),
        denied,
        capacity: MAX_LEASES,
      });
      return;
    }
    if (req.url.startsWith("/effect/")) {
      if (req.headers["x-poc-cell-key"] !== config.cellKey) {
        json(res, 401, {});
        return;
      }
      const id = req.url.slice("/effect/".length),
        lease = leases.get(id);
      if (!lease || Date.now() >= lease.expires) {
        if (lease) archive(lease, "deadline");
        denied.push({ id, status: 410 });
        if (denied.length > MAX_HISTORY) denied.shift();
        json(res, 410, {});
        return;
      }
      let body = "";
      req.on("data", (chunk) => {
        body += chunk;
        if (body.length > 4096) req.destroy();
      });
      req.on("end", () => {
        // Recheck after body input yields to timers/cancellation.
        if (!leases.has(id) || Date.now() >= lease.expires) {
          json(res, 410, {});
          return;
        }
        let call;
        try {
          call = JSON.parse(body);
        } catch {
          json(res, 400, {});
          return;
        }
        if (
          !["fetch", "identify"].includes(call.operation) ||
          (call.operation === "fetch" && call.url !== "http://synthetic/ok")
        ) {
          json(res, 403, {});
          return;
        }
        const allowed = lease.accepted < 3;
        lease.attempts.push({ operation: call.operation, allowed });
        if (!allowed) {
          json(res, 429, {});
          return;
        }
        lease.accepted++;
        json(res, 200, { subject: lease.subject, revision: lease.revision, cell: lease.cell });
      });
      return;
    }
    const end = req.url.indexOf("/", 1),
      cell = req.url.slice(1, end),
      path = req.url.slice(end);
    if (end < 0 || !cells.has(cell)) {
      json(res, 404, {});
      return;
    }
    if (leases.size >= MAX_LEASES) {
      json(res, 503, { error: "admission limit" });
      return;
    }
    const id = crypto.randomUUID(),
      revision = cells.get(cell).revision;
    // Synthetic caller authentication for tests; production must use its real auth.
    const subject =
      path === "/health" ? "alpha" : req.headers.authorization?.replace(/^Bearer /, "");
    if (subject !== "alpha" && subject !== "beta") {
      json(res, 401, {});
      return;
    }
    const lease = {
      id,
      cell,
      path,
      revision,
      subject,
      state: "running",
      accepted: 0,
      attempts: [],
      expires: Date.now() + (path === "/deadline" ? 200 : 3000),
    };
    function expire() {
      archive(lease, "deadline");
      lease.upstream?.destroy();
      if (!res.writableEnded) res.destroy();
    }
    lease.timer = setTimeout(expire, lease.expires - Date.now());
    leases.set(id, lease);
    const headers = { ...req.headers, "x-poc-lease": id, "x-poc-revision": revision };
    delete headers["x-poc-cell-key"];
    const upstream = http.request(
      { hostname: cell, port: 8080, path, method: req.method, headers },
      (response) => {
        res.writeHead(response.statusCode, response.headers);
        response.pipe(res);
        response.on("end", () => {
          if (!leases.has(id)) return;
          lease.state = "draining";
          lease.expires = Math.min(lease.expires, Date.now() + 200);
          clearTimeout(lease.timer);
          lease.timer = setTimeout(
            () => archive(lease, "background_deadline"),
            lease.expires - Date.now(),
          );
        });
      },
    );
    lease.upstream = upstream;
    upstream.on("error", () => {
      if (!res.headersSent && !res.destroyed) res.writeHead(502);
      if (!res.destroyed) res.end();
      archive(lease, "transport_failure");
    });
    res.on("close", () => {
      if (!res.writableEnded) {
        archive(lease, "cancelled");
        upstream.destroy();
      }
    });
    req.pipe(upstream);
  })
  .listen(8080, "0.0.0.0");
