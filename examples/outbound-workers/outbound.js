export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.protocol !== "https:" || url.hostname !== "api.example.com") {
      console.log("outbound denied", env.customerContext.customerId, url.hostname);
      return new Response("Destination not allowed", { status: 403 });
    }
    console.log("outbound allowed", env.customerContext.customerId, url.hostname);
    const headers = new Headers(request.headers);
    headers.set("Authorization", `Bearer ${env.API_TOKEN}`);
    return fetch(new Request(request, { headers }));
  },
};
