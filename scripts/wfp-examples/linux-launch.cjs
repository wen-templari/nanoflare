// Container-only forwarding for loopback fixture listeners. Publish these
// proxy ports on host loopback; the application/runtime API stays private.
const net = require("node:net");
const { spawn } = require("node:child_process");
const servers = [];
function forward(port, host, target, bind) {
  servers.push(
    net
      .createServer((socket) => {
        const upstream = net.connect(target, host);
        socket.pipe(upstream).pipe(socket);
        socket.on("error", () => upstream.destroy());
        upstream.on("error", () => socket.destroy());
        socket.on("close", () => upstream.destroy());
      })
      .listen(port, bind),
  );
}
forward(9000, "host.docker.internal", 9000, "127.0.0.1");
forward(54357, "127.0.0.1", 54347, "0.0.0.0");
forward(54358, "127.0.0.1", 54348, "0.0.0.0");
const child = spawn(
  "/fixture/nf-wfp-examples-linux",
  [
    "--workerd",
    "/fixture/linux-runtime/package/bin/workerd",
    "--state",
    "/state",
    "--publisher",
    "/fixture/reproducible-examples/worker-publisher-template/index.js",
    "--sites",
    "/fixture/reproducible-examples/internal-sites-template/index.js",
    "--port-base",
    "54347",
  ],
  { stdio: "inherit" },
);
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => child.kill(signal));
child.on("exit", (code) => {
  servers.forEach((server) => server.close());
  process.exit(code ?? 1);
});
