// Shared by both loader profiles. Diagnostics use the existing authenticated,
// redacted log pipeline; raw exceptions never become public response content.
export function exceptionMessage(error, phase, reference = "") {
  let detail;
  try {
    detail = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
  } catch {
    detail = "[unprintable exception]";
  }
  const message = `${phase}${reference ? ` [${reference}]` : ""}: ${detail}`;
  return new TextEncoder().encode(message).byteLength <= 4096
    ? message
    : `${phase}${reference ? ` [${reference}]` : ""}: [exception exceeds collection limit]`;
}
export function errorReporter(send, reference = "") {
  const reported = new WeakSet();
  return async (error, phase) => {
    try {
      if (error && (typeof error === "object" || typeof error === "function")) {
        if (reported.has(error)) return;
        reported.add(error);
      }
      await send("error", exceptionMessage(error, phase, reference));
    } catch {
      /* Reporting must never replace the application's failure. */
    }
  };
}
export function executionFailure(reference, status = 500) {
  return new Response(
    `Worker execution failed. Inspect nanoflare deployment output --level error. Reference: ${reference}`,
    {
      status,
      headers: {
        "content-type": "text/plain; charset=utf-8",
        "cache-control": "no-store",
        "X-Nanoflare-Error-Id": reference,
      },
    },
  );
}

export function observeResponseErrors(response, report, complete = () => {}) {
  if (!(response instanceof Response) || !response.body || response.status === 101) return response;
  const reader = response.body.getReader();
  let finished = false;
  const finish = () => {
    if (!finished) {
      finished = true;
      complete();
    }
  };
  return new Response(
    new ReadableStream({
      async pull(controller) {
        try {
          const part = await reader.read();
          if (part.done) {
            finish();
            controller.close();
          } else controller.enqueue(part.value);
        } catch (error) {
          await report(error, "response stream");
          finish();
          controller.error(error);
        }
      },
      async cancel(reason) {
        try {
          await reader.cancel(reason);
        } finally {
          finish();
        }
      },
    }),
    { status: response.status, statusText: response.statusText, headers: response.headers },
  );
}
