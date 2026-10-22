// Test-only isolation for the real CLI process. No Cloudflare login, telemetry
// endpoint or production API is needed for loopback compatibility qualification.
const os = require("node:os");
const net = require("node:net");
const { syncBuiltinESMExports } = require("node:module");
const directory = process.env.NF_WRANGLER_FIXTURE_DIRECTORY;
if (!directory) throw new Error("Wrangler qualification requires its disposable directory");
os.homedir = () => directory;
const connect = net.Socket.prototype.connect;
net.Socket.prototype.connect = function (...args) {
  const first = Array.isArray(args[0]) ? args[0][0] : args[0];
  const options = first && typeof first === "object" ? first : {};
  const socketPath =
    options.path || (typeof first === "string" && first.startsWith("/") ? first : undefined);
  if (!socketPath) {
    const host = options.host || (typeof args[1] === "string" ? args[1] : "localhost");
    if (!["localhost", "127.0.0.1", "::1", "::ffff:127.0.0.1"].includes(host)) {
      throw new Error("Wrangler qualification blocked a non-loopback connection");
    }
  }
  return connect.apply(this, args);
};
syncBuiltinESMExports();
