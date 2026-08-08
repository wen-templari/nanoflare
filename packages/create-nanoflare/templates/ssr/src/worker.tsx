import { Hono } from "hono";
import { renderToReadableStream } from "react-dom/server";

import styles from "./style.css?inline";
const app = new Hono();
function Page() {
  return (
    <html>
      <head>
        <style>{styles}</style>
      </head>
      <body>
        <main>
          <p>NANOFLARE / SSR</p>
          <h1>Rendered at the edge.</h1>
          <span>This React document came directly from a Hono Worker.</span>
        </main>
      </body>
    </html>
  );
}
app.get(
  "*",
  async (c) =>
    new Response(await renderToReadableStream(<Page />), {
      headers: { "content-type": "text/html; charset=utf-8" },
    }),
);
export default { fetch: app.fetch };
