function installEgressFetch(outbound) {
  globalThis.fetch = async function fetch(input, init) {
    return outbound().fetch(input, {
      redirect: input instanceof Request ? input.redirect : "follow",
      ...init,
    });
  };
}
