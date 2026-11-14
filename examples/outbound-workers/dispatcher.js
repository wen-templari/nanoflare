export default {
  fetch(request, env) {
    // A fixed trusted customer for this example. Authenticate and authorize
    // customer identity here before accepting user-selected script names.
    return env.CUSTOMERS.get(
      "demo",
      {},
      {
        outbound: { customerContext: { customerId: "demo" } },
      },
    ).fetch(request);
  },
};
