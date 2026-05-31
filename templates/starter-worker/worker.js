addEventListener("fetch", (event) => {
  event.respondWith(handleRequest(event.request));
});

async function handleRequest(request) {
  const identity = request.headers.get("x-platform-context");
  return new Response(
    JSON.stringify({
      message: "hello from platform",
      identity: identity ? JSON.parse(identity) : null,
    }),
    { headers: { "content-type": "application/json" } },
  );
}
