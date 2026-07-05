import { useEffect, useState } from "react";

type HelloPayload = {
  message: string;
  origin: string;
  path: string;
};

type TimePayload = {
  isoTime: string;
  method: string;
  pathname: string;
};

export function App() {
  const [hello, setHello] = useState<HelloPayload | null>(null);
  const [time, setTime] = useState<TimePayload | null>(null);
  const [status, setStatus] = useState("Loading Worker APIs...");

  useEffect(() => {
    let active = true;

    async function load() {
      try {
        const [helloResponse, timeResponse] = await Promise.all([
          fetch("/api/hello"),
          fetch("/api/time"),
        ]);

        if (!helloResponse.ok || !timeResponse.ok) {
          throw new Error("Worker API request failed");
        }

        const [helloPayload, timePayload] = await Promise.all([
          helloResponse.json() as Promise<HelloPayload>,
          timeResponse.json() as Promise<TimePayload>,
        ]);

        if (!active) return;

        setHello(helloPayload);
        setTime(timePayload);
        setStatus("Connected to the Worker API.");
      } catch (error) {
        if (!active) return;
        setStatus(error instanceof Error ? error.message : "Worker API unavailable");
      }
    }

    void load();
    return () => {
      active = false;
    };
  }, []);

  return (
    <main className="page">
      <section className="hero">
        <p className="eyebrow">Vite + React + Worker API</p>
        <h1>A single-page app with its API living next door.</h1>
        <p className="lede">
          The React app is bundled by Vite into static assets. The Worker in
          <code> worker/</code> answers <code>/api/*</code> requests and the SPA calls it directly.
        </p>
      </section>

      <section className="panel">
        <div className="status-row">
          <span className="status-dot" aria-hidden="true" />
          <p>{status}</p>
        </div>
        <div className="grid">
          <article className="card">
            <p className="label">Hello API</p>
            <strong>{hello?.message ?? "Waiting..."}</strong>
            <span>{hello ? `${hello.origin}${hello.path}` : "Fetching /api/hello"}</span>
          </article>
          <article className="card">
            <p className="label">Time API</p>
            <strong>{time ? new Date(time.isoTime).toLocaleTimeString() : "Waiting..."}</strong>
            <span>{time ? `${time.method} ${time.pathname}` : "Fetching /api/time"}</span>
          </article>
        </div>
      </section>
    </main>
  );
}
