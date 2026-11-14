import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

// The same deployment and invocation fixture runs against either management API.
// No credentials or existing resources are discovered implicitly.
export async function runBackend({
  baseURL,
  account,
  token,
  invokeURL,
  publishWorkersDev = false,
  fetcher = fetch,
  prefix = `nf-out-${randomUUID().slice(0, 8)}`,
}) {
  assert.ok(
    baseURL && account && token && invokeURL,
    "Explicit API URL, account, token and invocation URL template required",
  );
  assert.match(prefix, /^[a-z][a-z0-9-]{0,40}$/);
  assert.ok(invokeURL.includes("{script}"), "Invocation URL must contain {script}");
  const api = new URL(baseURL);
  assert.ok(
    api.protocol === "https:" || ["127.0.0.1", "localhost", "[::1]"].includes(api.hostname),
  );
  const root = `${api.href.replace(/\/$/, "")}/accounts/${encodeURIComponent(account)}`;
  const namespace = prefix,
    policy = prefix + "-policy",
    dispatcher = prefix + "-dispatcher";
  const observations = {},
    cleanup = [],
    cleanupErrors = [];
  const result = { status: "running", observations, cleanupErrors };
  async function call(path, method = "GET", body) {
    const response = await fetcher(root + path, {
      method,
      headers: {
        Authorization: `Bearer ${token}`,
        ...(body && !(body instanceof FormData) ? { "content-type": "application/json" } : {}),
      },
      body: body instanceof FormData ? body : body ? JSON.stringify(body) : undefined,
      redirect: "error",
    });
    const text = await response.text();
    let json;
    try {
      json = JSON.parse(text);
    } catch {}
    assert.ok(
      response.ok && json?.success !== false,
      `${method} ${path}: status ${response.status}`,
    );
    return json?.result;
  }
  function upload(code, bindings = []) {
    const form = new FormData();
    form.set(
      "metadata",
      new File(
        [JSON.stringify({ main_module: "worker.js", compatibility_date: "2025-12-10", bindings })],
        "metadata.json",
        { type: "application/json" },
      ),
    );
    form.set("worker.js", new File([code], "worker.js", { type: "application/javascript+module" }));
    return form;
  }
  try {
    for (const name of [policy, dispatcher]) {
      const response = await fetcher(root + `/workers/scripts/${name}`, {
        headers: { Authorization: `Bearer ${token}` },
        redirect: "error",
      });
      assert.equal(response.status, 404, "Disposable script name must not already exist");
    }
    await call("/workers/dispatch/namespaces", "POST", { name: namespace });
    cleanup.push(`/workers/dispatch/namespaces/${namespace}`);
    await call(
      `/workers/scripts/${policy}`,
      "PUT",
      upload(
        `export default {fetch(request,env){return Response.json({context:env.context,missing:env.missing??null},{status:new URL(request.url).pathname==='/block'?403:200});}}`,
      ),
    );
    cleanup.push(`/workers/scripts/${policy}`);
    await call(
      `/workers/dispatch/namespaces/${namespace}/scripts/customer`,
      "PUT",
      upload(
        `export default {fetch(request){return fetch('https://outbound-fixture.invalid'+new URL(request.url).pathname);}}`,
      ),
    );
    const binding = {
      name: "CUSTOMERS",
      type: "dispatch_namespace",
      namespace,
      outbound: { worker: { service: policy }, params: [{ name: "context" }, { name: "missing" }] },
    };
    await call(
      `/workers/scripts/${dispatcher}`,
      "PUT",
      upload(
        `export default {fetch(request,env){const url=new URL(request.url);return env.CUSTOMERS.get('customer',{}, {outbound:{context:JSON.parse(url.searchParams.get('context'))}}).fetch(request);}}`,
        [binding],
      ),
    );
    cleanup.push(`/workers/scripts/${dispatcher}`);
    const settings = await call(`/workers/scripts/${dispatcher}/settings`);
    const actual = settings.bindings?.find((item) => item.name === "CUSTOMERS");
    observations["metadata-wire"] = {
      namespaceMatches: actual?.namespace === namespace,
      serviceMatches: actual?.outbound?.worker?.service === policy,
      parameters: actual?.outbound?.params,
    };
    assert.deepEqual(observations["metadata-wire"], {
      namespaceMatches: true,
      serviceMatches: true,
      parameters: [{ name: "context" }, { name: "missing" }],
    });
    // Explicitly publishing workers.dev is confined to the new disposable dispatcher.
    if (publishWorkersDev)
      await call(`/workers/scripts/${dispatcher}/subdomain`, "POST", { enabled: true });
    async function invoke(path, context) {
      const url = new URL(invokeURL.replace("{script}", dispatcher));
      url.pathname = path;
      url.searchParams.set("context", JSON.stringify(context));
      const response = await fetcher(url, {
        redirect: "error",
        signal: AbortSignal.timeout(30000),
      });
      return { status: response.status, body: await response.json() };
    }
    const context = { customer: "fixture", nested: [1, true, null, { value: "x" }] };
    observations["binding-local"] = await invoke("/", context);
    observations["parameter-env"] = observations["binding-local"];
    observations["parameter-json"] = [];
    for (const value of [null, 7, "scalar", [1, { x: true }], context])
      observations["parameter-json"].push(await invoke("/", value));
    observations["parameter-missing"] = observations["binding-local"];
    observations["policy-block"] = await invoke("/block", context);
    assert.equal(observations["binding-local"].status, 200);
    assert.deepEqual(observations["binding-local"].body, { context, missing: null });
    assert.equal(observations["policy-block"].status, 403);
    result.status = "passed";
    return result;
  } catch (error) {
    Object.assign(result, { status: "failed", error: error.message });
    error.report = result;
    throw error;
  } finally {
    // Dispatcher first, namespace next, policy last; never delete pre-existing names.
    for (const path of cleanup.reverse())
      try {
        await call(path + (path.includes("/workers/scripts/") ? "?force=true" : ""), "DELETE");
      } catch (error) {
        cleanupErrors.push({ path, error: String(error.message) });
      }
    if (cleanupErrors.length && result.status === "passed") result.status = "cleanup-failed";
  }
}

export function compareObservations(left, right) {
  const ids = new Set([...Object.keys(left.observations), ...Object.keys(right.observations)]);
  return [...ids].map((id) => {
    if (!(id in left.observations) || !(id in right.observations))
      return { id, status: "not-compared" };
    try {
      assert.deepEqual(left.observations[id], right.observations[id]);
      return { id, status: "equal" };
    } catch {
      return {
        id,
        status: "different",
        left: left.observations[id],
        right: right.observations[id],
      };
    }
  });
}
