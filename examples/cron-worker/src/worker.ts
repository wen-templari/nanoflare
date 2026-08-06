type LastRun = {
  cron: string;
  fired_at: string;
  scheduled_time: number;
};

let lastRun: LastRun | null = null;

export default {
  async fetch(request) {
    const url = new URL(request.url);

    return Response.json({
      message: "cron worker ready",
      pathname: url.pathname,
      last_run: lastRun,
    });
  },

  async scheduled(controller, _env, ctx) {
    lastRun = {
      cron: controller.cron,
      fired_at: new Date().toISOString(),
      scheduled_time: controller.scheduledTime,
    };

    ctx.waitUntil(Promise.resolve(console.log("cron processed", lastRun)));
  },
} satisfies NanoflareWorkerHandler<Env>;
