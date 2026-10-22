// Real-runtime regression checks against the running local example fixture.
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
const config = JSON.parse(readFileSync(process.argv[2], "utf8"));
const evidence = [];
async function check(name, run) {
  await run();
  evidence.push({ name, passed: true });
  console.log(`PASS ${name}`);
}
const base = Number(process.env.NANOFLARE_EXAMPLES_PORT_BASE || 54337);
const publisher = `http://127.0.0.1:${base}`,
  sites = `http://127.0.0.1:${base + 1}`;
async function deploy(name, value) {
  const response = await fetch(publisher + "/deploy", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      scriptName: name,
      code: `export default {fetch(){return new Response(${JSON.stringify(value)})}}`,
    }),
  });
  assert.equal(response.status, 200, await response.text());
}
const names = ["regression-alpha", "regression-beta"];
await check("Publisher deploy, dispatch and independent update", async () => {
  await deploy(names[0], "alpha-v1");
  await deploy(names[1], "beta-v1");
  assert.equal(await (await fetch(publisher + "/" + names[0])).text(), "alpha-v1");
  await deploy(names[0], "alpha-v2");
  assert.equal(await (await fetch(publisher + "/" + names[0])).text(), "alpha-v2");
  assert.equal(await (await fetch(publisher + "/" + names[1])).text(), "beta-v1");
});
await check("Publisher management deletion", async () => {
  for (const name of names) {
    const response = await fetch(
      `${config.api_url}/accounts/${config.organization}/workers/dispatch/namespaces/my-dispatch-namespace/scripts/${name}`,
      { method: "DELETE", headers: { Authorization: "Bearer " + config.token } },
    );
    assert.equal(response.status, 200, await response.text());
    assert.notEqual((await fetch(publisher + "/" + name)).status, 200);
  }
});
await check("Sites denies unauthenticated and spoofed identity", async () => {
  for (const path of ["/deploy", "/sites/nanoflare-demo/"]) {
    const response = await fetch(sites + path, {
      redirect: "manual",
      headers: { "X-Nanoflare-User-JWT": "forged" },
    });
    assert.equal(response.status, 303);
    assert.equal(response.headers.get("location"), "/__nanoflare/login");
  }
});
const login = await fetch(sites + "/__nanoflare/login", {
  method: "POST",
  redirect: "manual",
  body: new URLSearchParams({
    email: "examples@nanoflare.local",
    password: "local-examples-password",
  }),
});
assert.equal(login.status, 303);
const cookie = login.headers.get("set-cookie").split(";")[0];
const auth = { Cookie: cookie };
await check("Sites rejects cross-origin and sandbox-origin mutations", async () => {
  for (const origin of ["null", "https://untrusted.example"]) {
    const response = await fetch(sites + "/api/sites/deploy", {
      method: "POST",
      headers: { ...auth, Origin: origin },
    });
    assert.equal(response.status, 403);
  }
});
async function siteUpload(marker) {
  const body = new FormData();
  body.set("name", "Regression Site");
  body.set("slug", "regression-site");
  body.set("paths", JSON.stringify(["index.html", "style.css", "empty.txt"]));
  body.append(
    "files",
    new File([`<!doctype html><title>${marker}</title><h1>${marker}</h1>`], "index.html", {
      type: "text/html",
    }),
  );
  body.append("files", new File(["body{color:navy}"], "style.css", { type: "text/css" }));
  body.append("files", new File([], "empty.txt", { type: "text/plain" }));
  const response = await fetch(sites + "/api/sites/deploy", {
    method: "POST",
    headers: auth,
    body,
  });
  assert.equal(response.status, 201, await response.text());
}
await check("Sites deploy, serve HTML/CSS/empty file and update", async () => {
  await siteUpload("site-v1");
  assert.match(
    await (await fetch(sites + "/sites/regression-site/", { headers: auth })).text(),
    /site-v1/,
  );
  assert.equal(
    await (await fetch(sites + "/sites/regression-site/style.css", { headers: auth })).text(),
    "body{color:navy}",
  );
  const empty = await fetch(sites + "/sites/regression-site/empty.txt", { headers: auth });
  assert.equal(empty.status, 200);
  assert.equal(await empty.text(), "");
  assert.equal(
    (await fetch(sites + "/sites/regression-site/missing", { headers: auth })).status,
    404,
  );
  await siteUpload("site-v2");
  assert.match(
    await (await fetch(sites + "/sites/regression-site/", { headers: auth })).text(),
    /site-v2/,
  );
  assert.match(
    await (await fetch(sites + "/admin", { headers: auth })).text(),
    /examples@nanoflare.local/,
  );
});
await check("Sites deletion removes Worker and database row", async () => {
  const response = await fetch(sites + "/api/sites/regression-site", {
    method: "DELETE",
    headers: auth,
  });
  assert.equal(response.status, 200, await response.text());
  assert.equal((await fetch(sites + "/api/sites/regression-site", { headers: auth })).status, 404);
  assert.equal((await fetch(sites + "/sites/regression-site/", { headers: auth })).status, 404);
});
if (process.argv[3])
  writeFileSync(
    process.argv[3],
    JSON.stringify({ at: new Date().toISOString(), checks: evidence }, null, 2) + "\n",
  );
