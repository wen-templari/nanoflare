// Local operational evidence sink; never forwards notifications externally.
const http = require("node:http");
const fs = require("node:fs");
http
  .createServer((req, res) => {
    if (req.method === "GET" && req.url === "/healthz") return res.end("ok");
    if (req.method !== "POST" || req.url !== "/alerts") {
      res.writeHead(404);
      return res.end();
    }
    let bytes = 0;
    const parts = [];
    req.on("data", (chunk) => {
      bytes += chunk.length;
      if (bytes > 1048576) req.destroy();
      else parts.push(chunk);
    });
    req.on("end", () => {
      try {
        const payload = JSON.parse(Buffer.concat(parts));
        // Keep only routing/state evidence, excluding arbitrary annotations.
        const record = {
          received_at: new Date().toISOString(),
          status: payload.status,
          alerts: payload.alerts.map((a) => ({ status: a.status, alertname: a.labels.alertname })),
        };
        const path = "/data/notifications.jsonl";
        if (fs.existsSync(path) && fs.statSync(path).size > 1048576)
          fs.renameSync(path, path + ".previous");
        fs.appendFileSync(path, JSON.stringify(record) + "\n");
        res.end("ok");
      } catch {
        res.writeHead(400);
        res.end();
      }
    });
  })
  .listen(8080, "0.0.0.0");
