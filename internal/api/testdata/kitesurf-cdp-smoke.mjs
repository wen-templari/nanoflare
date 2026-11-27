import assert from "node:assert/strict";
const server = process.argv[2];
const response = await fetch(server + "/health");
assert.equal(response.status, 200, await response.text());
const ws = new WebSocket(server.replace(/^http/, "ws") + "/devtools/browser");
const opened = new Promise((resolve, reject) => {
  ws.addEventListener("open", resolve, { once: true });
  ws.addEventListener("error", reject, { once: true });
});
let id = 0;
const pending = new Map();
ws.onmessage = ({ data }) => {
  const message = JSON.parse(data);
  pending.get(message.id)?.(message);
};
function call(method, params, sessionId) {
  const callId = ++id;
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      pending.delete(callId);
      reject(new Error("CDP timeout: " + method));
    }, 10000);
    pending.set(callId, (message) => {
      clearTimeout(timeout);
      pending.delete(callId);
      if (message.error) reject(new Error(method + ": " + JSON.stringify(message.error)));
      else resolve(message.result);
    });
    ws.send(JSON.stringify({ id: callId, method, params, sessionId }));
  });
}
try {
  await opened;
  const { targetId } = await call("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await call("Target.attachToTarget", { targetId, flatten: true });
  const page =
    "data:text/html," +
    encodeURIComponent("<title>Nanoflare Kitesurf probe</title><p>Controlled fixture</p>");
  const navigation = await call("Page.navigate", { url: page }, sessionId);
  assert.ok(!navigation.errorText, navigation.errorText);
  const result = await call(
    "Runtime.evaluate",
    { expression: "document.title", returnByValue: true },
    sessionId,
  );
  assert.equal(result.result.value, "Nanoflare Kitesurf probe", JSON.stringify(result));
  console.log("Kitesurf CDP on Nanoflare verified:", result.result.value);
} finally {
  ws.close();
}
