import type { KVNamespace, NanoflareEnv } from "@nanoflare/workers-types";

import { routeRequest } from "./router.js";

interface FullDemoEnv extends Omit<NanoflareEnv, "KV"> {
  VISITS_KV: KVNamespace;
}

export default {
  fetch(request: Request, env: FullDemoEnv): Promise<Response> {
    return routeRequest(request, env);
  },
};
