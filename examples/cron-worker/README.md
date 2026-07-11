# cron-worker

`cron-worker` is the smallest Nanoflare example focused on Cron Triggers.

## What It Demonstrates

- `triggers.crons` in `nanoflare.json`
- a Worker with both `fetch` and `scheduled` handlers
- inspecting the most recent scheduled invocation through an HTTP route
- local manual trigger testing through `/cdn-cgi/handler/scheduled`

## Setup

From this directory:

```sh
npm install
npm run build
nanoflare create
nanoflare deploy
```

The configured cron runs every five minutes in UTC:

```json
{
  "triggers": {
    "crons": ["*/5 * * * *"]
  }
}
```

## Routes To Try

- `/` returns the Worker status and the latest scheduled event seen by this isolate
- `/cdn-cgi/handler/scheduled?cron=*+*+*+*+*` manually invokes the scheduled handler during local development

## Where Logs Appear

Cron output is captured in the Worker detail page's Output tab alongside shared workerd output. The scheduler writes completion/failure lines there, and the example Worker writes `cron processed` from its `scheduled` handler.

`last_run` is stored in module memory for this tiny example. If the lazy local runtime idles out and restarts, `last_run` can return to `null`; use the Output tab as the reliable execution signal.
