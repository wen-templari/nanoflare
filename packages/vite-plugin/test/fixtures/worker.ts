export default {
  async fetch(request: Request, env: { MESSAGE: string; COUNT: number; CONFIG_MESSAGE: string }) {
    if (new URL(request.url).pathname === "/api/upload") {
      const value = (await request.formData()).get("image");
      return Response.json({
        isFile: value instanceof File,
        constructor: value?.constructor.name,
        name: value instanceof File ? value.name : null,
        contentType: request.headers.get("content-type"),
      });
    }

    return Response.json({
      message: env.MESSAGE,
      count: env.COUNT,
      configMessage: env.CONFIG_MESSAGE,
      pathname: new URL(request.url).pathname,
    });
  },
};
