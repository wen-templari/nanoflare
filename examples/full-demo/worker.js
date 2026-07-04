import { routeRequest } from "./router.js";

export default {
  fetch(request, env) {
    return routeRequest(request, env);
  },
};
