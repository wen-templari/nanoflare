// Native RPC propagates cancellation to the operation. Keep the caller's
// original reason when an RPC response stream disconnects during that abort.
export async function fetchWithSignal(request, send) {
  const signal = request.signal;
  if (!signal) return send(request);
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    let reader, controller;
    const cleanup = () => signal.removeEventListener("abort", abort);
    const abort = () => {
      cleanup();
      reject(signal.reason);
      controller?.error(signal.reason);
      reader?.cancel(signal.reason).catch(() => {});
    };
    signal.addEventListener("abort", abort, { once: true });
    Promise.resolve()
      .then(() => {
        signal.throwIfAborted();
        return send(request);
      })
      .then((response) => {
        if (signal.aborted) {
          response.body?.cancel().catch(() => {});
          return;
        }
        if (!response.body) {
          cleanup();
          resolve(response);
          return;
        }
        reader = response.body.getReader();
        const body = new ReadableStream({
          start(value) {
            controller = value;
          },
          async pull(value) {
            try {
              const part = await reader.read();
              if (signal.aborted) return;
              if (part.done) {
                cleanup();
                value.close();
              } else value.enqueue(part.value);
            } catch (error) {
              cleanup();
              if (!signal.aborted) value.error(error);
            }
          },
          cancel(reason) {
            cleanup();
            return reader.cancel(reason);
          },
        });
        resolve(
          new Response(body, {
            status: response.status,
            statusText: response.statusText,
            headers: response.headers,
          }),
        );
      })
      .catch((error) => {
        cleanup();
        reject(signal.aborted ? signal.reason : error);
      });
  });
}
