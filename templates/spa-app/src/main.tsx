import { createRoot } from "react-dom/client"; import "./style.css";
function App() { return <main><p>NANOFLARE / SPA</p><h1>Build the fast part in the browser.</h1><button onClick={() => fetch("/api/health").then((r) => r.json()).then((x) => alert(x.status))}>Check Worker API</button></main>; } createRoot(document.getElementById("root")!).render(<App />);
