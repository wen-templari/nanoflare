import { type FormEvent, type ReactNode, useEffect, useMemo, useState } from "react";
import {
  Activity, Archive, ArrowUpRight, Box, Boxes, Check, ChevronDown, CircleGauge,
  CloudUpload, Code2, Database, ExternalLink, FileCode2, FileText, FolderOpen,
  Globe2, HardDrive, Layers3, MoreHorizontal, Plus, RefreshCw, Search, Server,
  Settings, Sparkles, Trash2, UploadCloud, Waypoints, X,
} from "lucide-react";
import { Badge } from "./components/ui/badge";
import { Button } from "./components/ui/button";
import { Dialog } from "./components/ui/dialog";
import { Input } from "./components/ui/input";
import { cn } from "./lib/utils";

type Section = "overview" | "workers" | "pages" | "storage";
type Worker = { id: string; hostname: string; created_at: string; status?: "live" | "draft"; requests?: string; deployment?: string };
type Page = { id: number; name: string; path: string; worker: string; updated: string; status: "published" | "draft" };
type StoredObject = { id: number; name: string; type: string; size: string; updated: string };

const demoWorkers: Worker[] = [
  { id: "customer-portal", hostname: "portal.acme.internal", created_at: "2026-05-31T07:30:00Z", status: "live", requests: "24.8k", deployment: "deployment-8f2a91bc" },
  { id: "billing-sync", hostname: "billing.acme.internal", created_at: "2026-05-29T14:20:00Z", status: "live", requests: "8.2k", deployment: "deployment-c75d110e" },
  { id: "ops-dashboard", hostname: "ops.acme.internal", created_at: "2026-05-27T09:10:00Z", status: "draft", requests: "1.1k", deployment: "deployment-52e849aa" },
];

const initialPages: Page[] = [
  { id: 1, name: "Customer portal", path: "/", worker: "customer-portal", updated: "12 min ago", status: "published" },
  { id: 2, name: "Usage reports", path: "/reports", worker: "customer-portal", updated: "2 hr ago", status: "published" },
  { id: 3, name: "Invoice preview", path: "/preview", worker: "billing-sync", updated: "Yesterday", status: "draft" },
  { id: 4, name: "Operations board", path: "/", worker: "ops-dashboard", updated: "May 27", status: "published" },
];

const initialObjects: StoredObject[] = [
  { id: 1, name: "brand/wordmark.svg", type: "SVG", size: "18 KB", updated: "8 min ago" },
  { id: 2, name: "exports/may-usage.csv", type: "CSV", size: "2.4 MB", updated: "34 min ago" },
  { id: 3, name: "uploads/contract-v4.pdf", type: "PDF", size: "864 KB", updated: "Yesterday" },
  { id: 4, name: "avatars/team-grid.webp", type: "WEBP", size: "312 KB", updated: "May 28" },
  { id: 5, name: "docs/onboarding.md", type: "MD", size: "7 KB", updated: "May 25" },
];

const navItems: { section: Section; label: string; icon: typeof Server }[] = [
  { section: "overview", label: "Overview", icon: CircleGauge },
  { section: "workers", label: "Workers", icon: Waypoints },
  { section: "pages", label: "Pages", icon: Layers3 },
  { section: "storage", label: "Storage", icon: Database },
];

export function App() {
  const [section, setSection] = useState<Section>("overview");
  const [workers, setWorkers] = useState<Worker[]>(demoWorkers);
  const [pages, setPages] = useState(initialPages);
  const [objects, setObjects] = useState(initialObjects);
  const [workerDialog, setWorkerDialog] = useState(false);
  const [pageDialog, setPageDialog] = useState(false);
  const [uploadDialog, setUploadDialog] = useState(false);
  const [toast, setToast] = useState("");
  const [apiConnected, setApiConnected] = useState(false);

  useEffect(() => {
    fetch("/v1/apps")
      .then((response) => {
        if (!response.ok) throw new Error("API unavailable");
        return response.json() as Promise<Worker[]>;
      })
      .then((apps) => {
        setApiConnected(true);
        setWorkers(apps.map((app) => ({ ...app, status: "live", requests: "0", deployment: "awaiting deploy" })));
      })
      .catch(() => setApiConnected(false));
  }, []);

  function notify(message: string) {
    setToast(message);
    window.setTimeout(() => setToast(""), 2600);
  }

  return (
    <div className="console-grid min-h-screen">
      <aside className="nav-noise fixed inset-y-0 left-0 z-30 hidden w-60 flex-col bg-[#1c2926] text-[#e7e4da] lg:flex">
        <div className="flex h-20 items-center border-b border-white/10 px-5">
          <div className="grid size-9 place-items-center rounded-lg bg-[#e25b3f] text-white shadow-lg shadow-black/15"><Boxes className="size-5" /></div>
          <div className="ml-3">
            <p className="font-display text-xl leading-none">platform</p>
            <p className="mt-1 font-mono text-[9px] uppercase tracking-[0.18em] text-[#9eb0a8]">control plane</p>
          </div>
        </div>
        <nav className="flex-1 space-y-1 px-3 py-5">
          <p className="px-3 pb-2 font-mono text-[9px] uppercase tracking-[0.22em] text-[#83938e]">Workspace</p>
          {navItems.map(({ section: itemSection, label, icon: Icon }) => (
            <button
              key={itemSection}
              onClick={() => setSection(itemSection)}
              className={cn(
                "flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-[13px] font-semibold transition",
                section === itemSection ? "bg-white/11 text-white shadow-sm" : "text-[#aebdb7] hover:bg-white/6 hover:text-white",
              )}
            >
              <Icon className={cn("size-4", section === itemSection && "text-[#ee765c]")} />{label}
            </button>
          ))}
        </nav>
        <div className="m-3 rounded-lg border border-white/10 bg-black/10 p-3">
          <div className="flex items-center gap-2"><Activity className="size-3.5 text-[#78b88b]" /><span className="text-xs font-bold">All systems normal</span></div>
          <p className="mt-2 font-mono text-[9px] leading-4 text-[#8fa29b]">POOL 01 · 3 ISOLATES<br />REGION · LOCAL</p>
        </div>
        <button className="flex items-center gap-3 border-t border-white/10 px-5 py-4 text-xs font-semibold text-[#aebdb7] hover:text-white"><Settings className="size-4" />Settings</button>
      </aside>

      <main className="pb-20 lg:pb-0 lg:pl-60">
        <header className="sticky top-0 z-20 flex h-16 items-center justify-between border-b border-[#ded9ce] bg-[#f4f0e7]/85 px-4 backdrop-blur-md md:px-8">
          <div>
            <p className="font-mono text-[10px] uppercase tracking-[0.2em] text-[#90958e]">Local / <span className="text-[#cf563d]">{section}</span></p>
          </div>
          <div className="flex items-center gap-3">
            <div className={cn("hidden items-center gap-2 rounded-full border bg-white/50 px-3 py-1.5 text-[11px] font-bold sm:flex", apiConnected ? "border-[#bfd4c4] text-[#397046]" : "border-[#e1d2b8] text-[#9c7235]")}>
              <span className={cn("size-1.5 rounded-full", apiConnected ? "bg-[#52a46a]" : "bg-[#c89247]")} />
              {apiConnected ? "WORKER API CONNECTED" : "DEMO MODE"}
            </div>
            <button className="flex items-center gap-2 rounded-full bg-[#26332f] py-1.5 pl-1.5 pr-3 text-xs font-bold text-white">
              <span className="grid size-6 place-items-center rounded-full bg-[#e25b3f] text-[10px]">CL</span> clas <ChevronDown className="size-3" />
            </button>
          </div>
        </header>

        <div className="mx-auto max-w-7xl p-4 md:p-8">
          {section === "overview" && <Overview workers={workers} pages={pages} objects={objects} setSection={setSection} />}
          {section === "workers" && <Workers workers={workers} setWorkers={setWorkers} openDialog={() => setWorkerDialog(true)} notify={notify} apiConnected={apiConnected} />}
          {section === "pages" && <Pages pages={pages} setPages={setPages} openDialog={() => setPageDialog(true)} notify={notify} />}
          {section === "storage" && <Storage objects={objects} setObjects={setObjects} openDialog={() => setUploadDialog(true)} notify={notify} />}
        </div>
      </main>

      <nav className="fixed inset-x-3 bottom-3 z-30 flex justify-around rounded-xl border border-white/10 bg-[#1c2926]/95 p-1.5 text-[#aebdb7] shadow-2xl backdrop-blur-md lg:hidden">
        {navItems.map(({ section: itemSection, label, icon: Icon }) => (
          <button
            key={itemSection}
            onClick={() => setSection(itemSection)}
            className={cn("flex min-w-16 flex-col items-center gap-1 rounded-lg px-2 py-2 font-mono text-[8px] uppercase tracking-[0.1em] transition", section === itemSection && "bg-white/10 text-white")}
          >
            <Icon className={cn("size-4", section === itemSection && "text-[#ee765c]")} />{label}
          </button>
        ))}
      </nav>

      <CreateWorkerDialog open={workerDialog} onClose={() => setWorkerDialog(false)} workers={workers} setWorkers={setWorkers} notify={notify} apiConnected={apiConnected} />
      <CreatePageDialog open={pageDialog} onClose={() => setPageDialog(false)} setPages={setPages} workers={workers} notify={notify} />
      <UploadDialog open={uploadDialog} onClose={() => setUploadDialog(false)} setObjects={setObjects} notify={notify} />

      {toast && (
        <div className="fixed bottom-5 right-5 z-[60] flex items-center gap-2 rounded-lg bg-[#26332f] px-4 py-3 text-xs font-bold text-white shadow-xl">
          <Check className="size-4 text-[#8dc99b]" />{toast}
        </div>
      )}
    </div>
  );
}

function PageHeading({ eyebrow, title, copy, actions }: { eyebrow: string; title: string; copy: string; actions?: ReactNode }) {
  return (
    <div className="animate-rise mb-7 flex flex-col justify-between gap-4 md:flex-row md:items-end">
      <div>
        <p className="font-mono text-[10px] uppercase tracking-[0.2em] text-[#d75a41]">{eyebrow}</p>
        <h1 className="font-display mt-1 text-4xl tracking-[-0.04em] text-[#26332f] md:text-5xl">{title}</h1>
        <p className="mt-2 max-w-xl text-sm leading-6 text-[#7a8079]">{copy}</p>
      </div>
      {actions}
    </div>
  );
}

function Overview({ workers, pages, objects, setSection }: { workers: Worker[]; pages: Page[]; objects: StoredObject[]; setSection: (section: Section) => void }) {
  const stats = [
    { label: "Workers", value: workers.length, note: `${workers.filter((worker) => worker.status === "live").length} live · ${workers.filter((worker) => worker.status === "draft").length} draft`, icon: Waypoints, section: "workers" as Section },
    { label: "Published pages", value: pages.filter((page) => page.status === "published").length, note: `${pages.length} total routes`, icon: Globe2, section: "pages" as Section },
    { label: "Stored objects", value: objects.length, note: "3.6 MB in use", icon: HardDrive, section: "storage" as Section },
  ];
  return (
    <>
      <PageHeading eyebrow="Sunday, 31 May" title="Good afternoon, Clas." copy="Your private runtime is steady. Here is the shape of your workspace today." actions={<Button variant="dark"><Sparkles className="size-4" />Quick deploy</Button>} />
      <div className="grid gap-4 md:grid-cols-3">
        {stats.map(({ label, value, note, icon: Icon, section: target }, index) => (
          <button key={label} onClick={() => setSection(target)} style={{ animationDelay: `${index * 80}ms` }} className="paper-panel animate-rise group rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85 p-5 text-left transition hover:-translate-y-0.5 hover:border-[#c7c0b4]">
            <div className="flex justify-between"><Icon className="size-5 text-[#d75a41]" /><ArrowUpRight className="size-4 text-[#b8b7b0] transition group-hover:text-[#d75a41]" /></div>
            <p className="mt-8 font-display text-5xl tracking-[-0.06em]">{value}</p>
            <p className="mt-2 text-sm font-extrabold">{label}</p><p className="mt-1 font-mono text-[10px] text-[#91958e]">{note}</p>
          </button>
        ))}
      </div>
      <div className="mt-6 grid gap-6 lg:grid-cols-[1.5fr_1fr]">
        <Panel title="Runtime activity" eyebrow="Last 24 hours">
          <div className="flex h-52 items-end gap-2 px-1 pt-7">
            {[35, 44, 37, 58, 65, 52, 76, 68, 88, 72, 82, 96, 77, 64, 73, 56, 61, 49, 66, 72, 60, 52, 44, 59].map((height, index) => <div key={index} className="group relative flex-1 rounded-t bg-[#d7ded8] transition hover:bg-[#e25b3f]" style={{ height: `${height}%` }} />)}
          </div>
          <div className="mt-3 flex justify-between font-mono text-[9px] text-[#9ba09a]"><span>12 AM</span><span>6 AM</span><span>12 PM</span><span>NOW</span></div>
        </Panel>
        <Panel title="Recent events" eyebrow="Live log">
          <Event icon={<CloudUpload />} text="may-usage.csv uploaded" time="34m" />
          <Event icon={<Globe2 />} text="portal route published" time="2h" />
          <Event icon={<Code2 />} text="billing-sync deployed" time="5h" />
          <Event icon={<Archive />} text="backup completed" time="8h" />
        </Panel>
      </div>
    </>
  );
}

function Workers({ workers, setWorkers, openDialog, notify, apiConnected }: { workers: Worker[]; setWorkers: (workers: Worker[]) => void; openDialog: () => void; notify: (text: string) => void; apiConnected: boolean }) {
  return (
    <>
      <PageHeading eyebrow="Runtime" title="Workers" copy="Register isolated services, deploy bundles, and watch the runtime pool." actions={<Button onClick={openDialog}><Plus className="size-4" />New worker</Button>} />
      <Panel title={`${workers.length} registered workers`} eyebrow={apiConnected ? "Live inventory" : "Demo inventory"} flush>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[720px] text-left">
            <thead><tr className="border-b border-[#e3ded3] font-mono text-[9px] uppercase tracking-[0.14em] text-[#989b95]"><th className="px-5 py-3">Worker</th><th>State</th><th>Requests</th><th>Deployment</th><th>Created</th><th className="pr-4 text-right">Actions</th></tr></thead>
            <tbody>{workers.map((worker) => <WorkerRow key={worker.id} worker={worker} setWorkers={setWorkers} workers={workers} notify={notify} />)}</tbody>
          </table>
        </div>
      </Panel>
    </>
  );
}

function WorkerRow({ worker, workers, setWorkers, notify }: { worker: Worker; workers: Worker[]; setWorkers: (workers: Worker[]) => void; notify: (text: string) => void }) {
  const [deploying, setDeploying] = useState(false);
  function deploy() {
    setDeploying(true);
    window.setTimeout(() => { setDeploying(false); notify(`${worker.id} deployment queued`); }, 700);
  }
  return (
    <tr className="border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/55">
      <td className="px-5 py-4"><p className="font-extrabold text-[#35413e]">{worker.id}</p><p className="mt-1 font-mono text-[10px] text-[#949891]">{worker.hostname}</p></td>
      <td><Badge tone={worker.status === "draft" ? "orange" : "green"}>{worker.status ?? "live"}</Badge></td>
      <td className="font-mono text-[11px]">{worker.requests ?? "0"}</td>
      <td className="font-mono text-[10px] text-[#727a74]">{worker.deployment ?? "awaiting deploy"}</td>
      <td className="text-[#7d837d]">{new Date(worker.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</td>
      <td className="pr-4 text-right"><Button variant="ghost" size="sm" onClick={deploy} disabled={deploying}>{deploying ? <RefreshCw className="size-3 animate-spin" /> : <UploadCloud className="size-3" />}{deploying ? "Queuing" : "Deploy"}</Button><Button variant="ghost" size="icon" aria-label={`Delete ${worker.id}`} onClick={() => { setWorkers(workers.filter(({ id }) => id !== worker.id)); notify(`${worker.id} removed`); }}><Trash2 className="size-3.5" /></Button></td>
    </tr>
  );
}

function Pages({ pages, setPages, openDialog, notify }: { pages: Page[]; setPages: (pages: Page[]) => void; openDialog: () => void; notify: (text: string) => void }) {
  return (
    <>
      <PageHeading eyebrow="Routing" title="Pages" copy="Map public paths to workers and keep published routes in view." actions={<Button onClick={openDialog}><Plus className="size-4" />New page</Button>} />
      <Panel title={`${pages.length} routes`} eyebrow="Published surfaces" flush>
        <div className="divide-y divide-[#e5dfd4]">
          {pages.map((page) => (
            <div key={page.id} className="group flex flex-col gap-3 px-5 py-4 transition hover:bg-white/55 sm:flex-row sm:items-center">
              <div className="grid size-10 place-items-center rounded-lg border border-[#ded9ce] bg-white/75 text-[#d75a41]"><FileCode2 className="size-4" /></div>
              <div className="min-w-0 flex-1"><div className="flex items-center gap-2"><p className="text-sm font-extrabold">{page.name}</p><Badge tone={page.status === "published" ? "green" : "orange"}>{page.status}</Badge></div><p className="mt-1 font-mono text-[10px] text-[#8d928c]">{page.worker} → {page.path}</p></div>
              <p className="font-mono text-[10px] text-[#a0a29c]">{page.updated}</p>
              <Button variant="ghost" size="icon" aria-label="Open route"><ExternalLink className="size-3.5" /></Button>
              <Button variant="ghost" size="icon" aria-label={`Delete ${page.name}`} onClick={() => { setPages(pages.filter(({ id }) => id !== page.id)); notify(`${page.name} removed`); }}><Trash2 className="size-3.5" /></Button>
            </div>
          ))}
        </div>
      </Panel>
    </>
  );
}

function Storage({ objects, setObjects, openDialog, notify }: { objects: StoredObject[]; setObjects: (objects: StoredObject[]) => void; openDialog: () => void; notify: (text: string) => void }) {
  const [query, setQuery] = useState("");
  const visibleObjects = useMemo(() => objects.filter((object) => object.name.includes(query.toLowerCase())), [objects, query]);
  return (
    <>
      <PageHeading eyebrow="Object store" title="Storage" copy="Browse application files, inspect assets, and keep object storage tidy." actions={<Button onClick={openDialog}><UploadCloud className="size-4" />Upload object</Button>} />
      <div className="mb-4 flex items-center gap-3 rounded-lg border border-[#dbd5ca] bg-white/55 p-2">
        <Search className="ml-2 size-4 text-[#959a93]" /><Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search objects by path..." className="h-8 border-0 bg-transparent p-0 focus:ring-0" />
      </div>
      <Panel title={`${visibleObjects.length} objects`} eyebrow="platform-assets" flush>
        <div className="divide-y divide-[#e5dfd4]">
          {visibleObjects.map((object) => (
            <div key={object.id} className="group flex items-center gap-3 px-5 py-4 transition hover:bg-white/55">
              <div className="grid size-10 place-items-center rounded-lg border border-[#ded9ce] bg-white/75 text-[#52756e]"><FileText className="size-4" /></div>
              <div className="min-w-0 flex-1"><p className="truncate text-sm font-extrabold">{object.name}</p><p className="mt-1 font-mono text-[10px] text-[#92968f]">{object.type} · {object.size}</p></div>
              <p className="hidden font-mono text-[10px] text-[#a0a29c] sm:block">{object.updated}</p>
              <Button variant="ghost" size="icon" aria-label="More options"><MoreHorizontal className="size-4" /></Button>
              <Button variant="ghost" size="icon" aria-label={`Delete ${object.name}`} onClick={() => { setObjects(objects.filter(({ id }) => id !== object.id)); notify(`${object.name} deleted`); }}><Trash2 className="size-3.5" /></Button>
            </div>
          ))}
        </div>
      </Panel>
    </>
  );
}

function Panel({ title, eyebrow, children, flush = false }: { title: string; eyebrow: string; children: ReactNode; flush?: boolean }) {
  return (
    <section className="paper-panel animate-rise overflow-hidden rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85">
      <header className="flex items-center justify-between border-b border-[#e7e1d6] px-5 py-4"><div><p className="font-mono text-[9px] uppercase tracking-[0.18em] text-[#d35c45]">{eyebrow}</p><h2 className="mt-1 text-sm font-extrabold">{title}</h2></div><MoreHorizontal className="size-4 text-[#a1a49d]" /></header>
      <div className={flush ? "" : "p-5"}>{children}</div>
    </section>
  );
}

function Event({ icon, text, time }: { icon: ReactNode; text: string; time: string }) {
  return <div className="flex items-center gap-3 border-b border-[#ece7dc] py-3 last:border-0 [&_svg]:size-3.5 [&_svg]:text-[#d65c44]"><div className="grid size-8 place-items-center rounded-full bg-[#f3e5df]">{icon}</div><p className="flex-1 text-xs font-bold">{text}</p><span className="font-mono text-[9px] text-[#a1a49e]">{time}</span></div>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block"><span className="mb-1.5 block font-mono text-[10px] uppercase tracking-[0.14em] text-[#7e847d]">{label}</span>{children}</label>;
}

function CreateWorkerDialog({ open, onClose, workers, setWorkers, notify, apiConnected }: { open: boolean; onClose: () => void; workers: Worker[]; setWorkers: (workers: Worker[]) => void; notify: (text: string) => void; apiConnected: boolean }) {
  const [id, setID] = useState("");
  const [hostname, setHostname] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    const worker = { id, hostname, created_at: new Date().toISOString(), status: "draft" as const, requests: "0", deployment: "awaiting deploy" };
    if (apiConnected) {
      const response = await fetch("/v1/apps", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ id, hostname }) });
      if (!response.ok) return notify("Worker registration failed");
    }
    setWorkers([...workers, worker]); setID(""); setHostname(""); onClose(); notify(`${id} registered`);
  }
  return <Dialog open={open} onClose={onClose} title="Register worker" description="Create an isolated runtime target. You can deploy a worker bundle after registration."><form className="space-y-4" onSubmit={submit}><Field label="Worker ID"><Input required pattern="[a-z][a-z0-9-]*" placeholder="analytics-worker" value={id} onChange={(event) => setID(event.target.value)} /></Field><Field label="Hostname"><Input required placeholder="analytics.acme.internal" value={hostname} onChange={(event) => setHostname(event.target.value)} /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Register worker</Button></div></form></Dialog>;
}

function CreatePageDialog({ open, onClose, setPages, workers, notify }: { open: boolean; onClose: () => void; setPages: React.Dispatch<React.SetStateAction<Page[]>>; workers: Worker[]; notify: (text: string) => void }) {
  const [name, setName] = useState(""); const [path, setPath] = useState("/");
  function submit(event: FormEvent) { event.preventDefault(); setPages((pages) => [...pages, { id: Date.now(), name, path, worker: workers[0]?.id ?? "unassigned", updated: "Just now", status: "draft" }]); onClose(); notify(`${name} route created`); setName(""); setPath("/"); }
  return <Dialog open={open} onClose={onClose} title="Add page route" description="Create a route mapping. New surfaces start as drafts until published."><form className="space-y-4" onSubmit={submit}><Field label="Page name"><Input required placeholder="Team dashboard" value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label="Public path"><Input required placeholder="/dashboard" value={path} onChange={(event) => setPath(event.target.value)} /></Field><Field label="Worker"><select className="h-10 w-full rounded-md border border-[#d6d0c3] bg-white/80 px-3 text-sm outline-none">{workers.map((worker) => <option key={worker.id}>{worker.id}</option>)}</select></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Create route</Button></div></form></Dialog>;
}

function UploadDialog({ open, onClose, setObjects, notify }: { open: boolean; onClose: () => void; setObjects: React.Dispatch<React.SetStateAction<StoredObject[]>>; notify: (text: string) => void }) {
  const [path, setPath] = useState("");
  function submit(event: FormEvent) { event.preventDefault(); setObjects((objects) => [{ id: Date.now(), name: path, type: path.split(".").pop()?.toUpperCase() ?? "FILE", size: "0 KB", updated: "Just now" }, ...objects]); onClose(); notify(`${path} uploaded`); setPath(""); }
  return <Dialog open={open} onClose={onClose} title="Upload object" description="Add an object path to the shared MinIO-backed asset bucket."><form className="space-y-4" onSubmit={submit}><div className="grid place-items-center rounded-lg border border-dashed border-[#c9c2b6] bg-white/45 px-4 py-7 text-center"><FolderOpen className="size-7 text-[#d35c45]" /><p className="mt-3 text-xs font-bold">Choose a destination path</p><p className="mt-1 font-mono text-[9px] text-[#999c96]">OBJECT CONTENT UPLOAD WILL USE A PRESIGNED URL</p></div><Field label="Object path"><Input required placeholder="uploads/report.csv" value={path} onChange={(event) => setPath(event.target.value)} /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Upload object</Button></div></form></Dialog>;
}
