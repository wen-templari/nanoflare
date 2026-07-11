interface Env {}

interface ScheduledController {
  cron: string
  scheduledTime: number
}

interface ExecutionContext {
  waitUntil(promise: Promise<unknown>): void
}

type LastRun = {
  cron: string
  fired_at: string
  scheduled_time: number
}

let lastRun: LastRun | null = null

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url)

    return Response.json({
      message: "cron worker ready",
      pathname: url.pathname,
      last_run: lastRun,
    })
  },

  async scheduled(controller: ScheduledController, _env: Env, ctx: ExecutionContext): Promise<void> {
    lastRun = {
      cron: controller.cron,
      fired_at: new Date().toISOString(),
      scheduled_time: controller.scheduledTime,
    }

    ctx.waitUntil(Promise.resolve(console.log("cron processed", lastRun)))
  },
}
