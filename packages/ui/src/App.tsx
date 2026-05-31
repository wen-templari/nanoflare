import { type FormEvent, type ReactNode, useEffect, useMemo, useState } from "react";
import {
  Activity, Archive, ArrowLeft, ArrowUpRight, BarChart3, Boxes, Check,
  ChevronDown, ChevronRight, CircleGauge, CloudUpload, Code2, Copy, Database,
  ExternalLink, FileCode2, FileJson, FileText, Folder, FolderOpen, GitBranch,
  Globe2, HardDrive, Layers3, MoreHorizontal, Plus, RefreshCw, Search,
  Server, Settings, SlidersHorizontal, Terminal, Timer, Trash2,
  UploadCloud, Waypoints,
} from "lucide-react";
import { Badge } from "./components/ui/badge";
import { Button } from "./components/ui/button";
import { Dialog } from "./components/ui/dialog";
import { Input } from "./components/ui/input";
import { cn } from "./lib/utils";

type Section = "overview" | "workers" | "pages" | "storage" | "monitoring";
type Worker = { id: string; name: string; hostname: string; created_at: string; status?: "live" | "draft"; requests?: string; deployment?: string };
type WorkerDetailTab = "files" | "config" | "deployments" | "output";
type WorkerDeployment = { id: string; entrypoint: string; bundle_size: number; compatibility_date: string; created_at: string };
type WorkerDetailData = { app: Worker; deployment?: WorkerDeployment };
type ConsoleDeployment = { id: string; app_id: string; app_name: string; hostname: string; entrypoint: string; bundle_size: number; compatibility_date: string; state: "active" | "inactive"; created_at: string };
type WorkerFile = { name: string; path: string; size: number; content: string };
type WorkerOutputLine = { timestamp: string; level: string; message: string };
type WorkerTraffic = {
  available: boolean;
  requests_per_second: number;
  p95_latency: number;
  error_rate: number;
  traffic: number[];
  status_codes: { code: string; value: number }[];
};
type Page = { id: number; name: string; path: string; worker: string; updated: string; status: "published" | "draft" };
type StoredObject = { id: number; name: string; type: string; size: string; updated: string };
type PrometheusValue = [number, string];
type PrometheusResult = { metric: Record<string, string>; value?: PrometheusValue; values?: PrometheusValue[] };
type PrometheusResponse = { status: "success" | "error"; data?: { result: PrometheusResult[] } };
type MonitoringData = {
  available: boolean;
  requestsPerSecond: number;
  p95Latency: number;
  errorRate: number;
  openConnections: number;
  traffic: number[];
  statusCodes: { code: string; value: number }[];
};

const demoWorkers: Worker[] = [
  { id: "ec84a0260cb606a15cf3a09ea938ddd1ca3a57089320af23", name: "Customer portal", hostname: "portal.acme.internal", created_at: "2026-05-31T07:30:00Z", status: "live", requests: "24.8k", deployment: "cc01a1bab53a42c865bfe59a2296cd28419d03db91a54ac8" },
  { id: "dc2b346df24d2e247917fcdbdc344e12cb6b642902ed86f8", name: "Billing sync", hostname: "billing.acme.internal", created_at: "2026-05-29T14:20:00Z", status: "live", requests: "8.2k", deployment: "06b83b2017e634e152d296a447660e9567c9f317fd2e47d5" },
  { id: "ce586cdb38f47b15dc20397f081eef8738f1231f7df55f0a", name: "Operations dashboard", hostname: "ops.acme.internal", created_at: "2026-05-27T09:10:00Z", status: "draft", requests: "1.1k", deployment: "42eac2d9c9b5672eb56237745cbeef42cf8fe09107567c44" },
];

const demoDeployments: ConsoleDeployment[] = [
  { id: "cc01a1bab53a42c865bfe59a2296cd28419d03db91a54ac8", app_id: demoWorkers[0].id, app_name: demoWorkers[0].name, hostname: demoWorkers[0].hostname, entrypoint: "worker.js", bundle_size: 18432, compatibility_date: "2026-05-31", state: "active", created_at: "2026-05-31T07:30:00Z" },
  { id: "06b83b2017e634e152d296a447660e9567c9f317fd2e47d5", app_id: demoWorkers[1].id, app_name: demoWorkers[1].name, hostname: demoWorkers[1].hostname, entrypoint: "worker.js", bundle_size: 9638, compatibility_date: "2026-05-29", state: "active", created_at: "2026-05-29T14:20:00Z" },
  { id: "42eac2d9c9b5672eb56237745cbeef42cf8fe09107567c44", app_id: demoWorkers[2].id, app_name: demoWorkers[2].name, hostname: demoWorkers[2].hostname, entrypoint: "worker.js", bundle_size: 6144, compatibility_date: "2026-05-27", state: "inactive", created_at: "2026-05-27T09:10:00Z" },
];

const initialPages: Page[] = [
  { id: 1, name: "Customer portal", path: "/", worker: demoWorkers[0].id, updated: "12 min ago", status: "published" },
  { id: 2, name: "Usage reports", path: "/reports", worker: demoWorkers[0].id, updated: "2 hr ago", status: "published" },
  { id: 3, name: "Invoice preview", path: "/preview", worker: demoWorkers[1].id, updated: "Yesterday", status: "draft" },
  { id: 4, name: "Operations board", path: "/", worker: demoWorkers[2].id, updated: "May 27", status: "published" },
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
  { section: "monitoring", label: "Monitoring", icon: BarChart3 },
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
    let cancelled = false;
    async function refreshWorkers() {
      try {
        const response = await fetch("/v1/apps");
        if (!response.ok) throw new Error("API unavailable");
        const apps = await response.json() as Worker[];
        if (cancelled) return;
        setApiConnected(true);
        setWorkers(apps);
        const nextWorkers = await Promise.all(apps.map(async (app) => {
          const [detail, traffic] = await Promise.all([
            fetchJSON<WorkerDetailData>(`/v1/apps/${app.id}`).catch(() => undefined),
            fetchJSON<WorkerTraffic>(`/v1/apps/${app.id}/traffic`).catch(() => undefined),
          ]);
          return {
            ...app,
            status: detail?.deployment ? "live" as const : "draft" as const,
            requests: traffic?.available ? `${traffic.requests_per_second.toFixed(2)}/s` : "unavailable",
            deployment: detail?.deployment?.id ?? "awaiting deploy",
          };
        }));
        if (!cancelled) setWorkers(nextWorkers);
      } catch {
        if (!cancelled) setApiConnected(false);
      }
    }
    void refreshWorkers();
    const interval = window.setInterval(() => void refreshWorkers(), 15000);
    return () => { cancelled = true; window.clearInterval(interval); };
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
          {section === "pages" && <Pages pages={pages} setPages={setPages} workers={workers} openDialog={() => setPageDialog(true)} notify={notify} />}
          {section === "storage" && <Storage objects={objects} setObjects={setObjects} openDialog={() => setUploadDialog(true)} notify={notify} />}
          {section === "monitoring" && <Monitoring />}
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
      <PageHeading eyebrow="Sunday, 31 May" title="Good afternoon, Clas." copy="Your private runtime is steady. Here is the shape of your workspace today." />
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
  const [selectedWorker, setSelectedWorker] = useState<Worker>();
  if (selectedWorker) {
    return <WorkerDetail worker={selectedWorker} onBack={() => setSelectedWorker(undefined)} notify={notify} />;
  }
  return (
    <>
      <PageHeading eyebrow="Runtime" title="Workers" copy="Register isolated services, deploy bundles, and watch the runtime pool." actions={<Button onClick={openDialog}><Plus className="size-4" />New worker</Button>} />
      <Panel title={`${workers.length} registered workers`} eyebrow={apiConnected ? "Live inventory" : "Demo inventory"} flush>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[720px] text-left">
            <thead><tr className="border-b border-[#e3ded3] font-mono text-[9px] uppercase tracking-[0.14em] text-[#989b95]"><th className="px-5 py-3">Worker</th><th>State</th><th>Requests</th><th>Deployment</th><th>Created</th><th className="pr-4 text-right">Actions</th></tr></thead>
            <tbody>{workers.map((worker) => <WorkerRow key={worker.id} worker={worker} setWorkers={setWorkers} workers={workers} notify={notify} onSelect={() => setSelectedWorker(worker)} />)}</tbody>
          </table>
        </div>
      </Panel>
    </>
  );
}

function WorkerRow({ worker, workers, setWorkers, notify, onSelect }: { worker: Worker; workers: Worker[]; setWorkers: (workers: Worker[]) => void; notify: (text: string) => void; onSelect: () => void }) {
  return (
    <tr className="cursor-pointer border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/70" onClick={onSelect}>
      <td className="px-5 py-4"><div className="flex items-center gap-3"><div><p className="font-extrabold text-[#35413e]">{worker.name}</p><p className="mt-1 font-mono text-[10px] text-[#949891]">{worker.hostname}</p></div><ChevronRight className="size-3.5 text-[#c0beb6] transition group-hover:translate-x-0.5" /></div></td>
      <td><Badge tone={worker.status === "draft" ? "orange" : "green"}>{worker.status ?? "live"}</Badge></td>
      <td className="font-mono text-[11px]">{worker.requests ?? "0"}</td>
      <td className="font-mono text-[10px] text-[#727a74]">{worker.deployment ?? "awaiting deploy"}</td>
      <td className="text-[#7d837d]">{new Date(worker.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</td>
      <td className="pr-4 text-right"><Button variant="ghost" size="icon" aria-label={`Delete ${worker.name}`} onClick={(event) => { event.stopPropagation(); setWorkers(workers.filter(({ id }) => id !== worker.id)); notify(`${worker.name} removed`); }}><Trash2 className="size-3.5" /></Button></td>
    </tr>
  );
}

function WorkerDetail({ worker, onBack, notify }: { worker: Worker; onBack: () => void; notify: (text: string) => void }) {
  const [tab, setTab] = useState<WorkerDetailTab>("files");
  const [detail, setDetail] = useState<WorkerDetailData>();
  const [files, setFiles] = useState<WorkerFile[]>([]);
  const [deployments, setDeployments] = useState<ConsoleDeployment[]>(() => demoDeployments.filter((deployment) => deployment.app_id === worker.id));
  const [selectedFile, setSelectedFile] = useState<WorkerFile>();
  const [output, setOutput] = useState<WorkerOutputLine[]>([]);
  const [traffic, setTraffic] = useState<WorkerTraffic>({ available: false, requests_per_second: 0, p95_latency: 0, error_rate: 0, traffic: [], status_codes: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    async function refresh() {
      try {
        const [nextDetail, nextFiles, nextDeployments, nextOutput, nextTraffic] = await Promise.all([
          fetchJSON<WorkerDetailData>(`/v1/apps/${worker.id}`),
          fetchJSON<WorkerFile[]>(`/v1/apps/${worker.id}/files`),
          fetchJSON<ConsoleDeployment[]>(`/v1/apps/${worker.id}/deployments`),
          fetchJSON<WorkerOutputLine[]>(`/v1/apps/${worker.id}/output`),
          fetchJSON<WorkerTraffic>(`/v1/apps/${worker.id}/traffic`),
        ]);
        if (cancelled) return;
        setDetail(nextDetail); setFiles(nextFiles); setDeployments(nextDeployments); setOutput(nextOutput); setTraffic(nextTraffic); setError("");
        setSelectedFile((current) => nextFiles.find((file) => file.path === current?.path) ?? nextFiles[0]);
      } catch {
        if (!cancelled) setError("Worker detail API unavailable");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    void refresh();
    const interval = window.setInterval(() => void refresh(), 15000);
    return () => { cancelled = true; window.clearInterval(interval); };
  }, [worker.id]);

  const deployment = detail?.deployment;
  const cards = [
    { label: "Request rate", value: `${traffic.requests_per_second.toFixed(2)}/s`, icon: Activity },
    { label: "P95 latency", value: formatDuration(traffic.p95_latency), icon: Timer },
    { label: "Error rate", value: `${(traffic.error_rate * 100).toFixed(2)}%`, icon: CircleGauge },
    { label: "Bundle size", value: formatBytes(deployment?.bundle_size ?? 0), icon: Archive },
  ];

  return (
    <>
      <button onClick={onBack} className="animate-rise mb-5 flex items-center gap-2 font-mono text-[10px] font-bold uppercase tracking-[0.14em] text-[#77817a] transition hover:text-[#d75a41]"><ArrowLeft className="size-3.5" />All workers</button>
      <div className="animate-rise mb-6 flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <div className="flex flex-wrap items-center gap-2"><p className="font-mono text-[10px] uppercase tracking-[0.2em] text-[#d75a41]">Worker isolate</p><Badge tone={deployment ? "green" : "orange"}>{deployment ? "live" : "draft"}</Badge></div>
          <h1 className="font-display mt-2 text-4xl tracking-[-0.04em] text-[#26332f] md:text-5xl">{worker.name}</h1>
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 font-mono text-[10px] text-[#858b84]"><span className="flex items-center gap-1.5"><Globe2 className="size-3" />{worker.hostname}</span><span className="flex items-center gap-1.5"><GitBranch className="size-3" />{deployment?.id ?? "awaiting deploy"}</span></div>
        </div>
        <Button variant="outline" onClick={() => notify(`${worker.name} hostname copied`)}><Copy className="size-3.5" />Copy hostname</Button>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map(({ label, value, icon: Icon }, index) => <div key={label} style={{ animationDelay: `${index * 60}ms` }} className="paper-panel animate-rise rounded-lg border border-[#dcd6ca] bg-[#fbf9f3]/85 p-4"><div className="flex items-center justify-between"><p className="font-mono text-[9px] uppercase tracking-[0.14em] text-[#90958e]">{label}</p><Icon className="size-3.5 text-[#d75a41]" /></div><p className="mt-3 font-display text-3xl tracking-[-0.04em]">{value}</p></div>)}
      </div>

      {(loading || error) && <div className={cn("mt-6 rounded-lg border px-4 py-3 font-mono text-[10px] uppercase tracking-[0.12em]", error ? "border-[#ecc3b6] bg-[#fae5df] text-[#b14b37]" : "border-[#dcd6ca] bg-white/45 text-[#8c918b]")}>{error || "Loading worker detail from platformd"}</div>}

      <div className="mt-6 grid gap-6 xl:grid-cols-[1.65fr_1fr]">
        <section className="paper-panel animate-rise overflow-hidden rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85">
          <header className="flex flex-wrap items-center justify-between gap-3 border-b border-[#e7e1d6] px-4 py-3">
            <div className="flex gap-1">{([{ id: "files", label: "Files", icon: FileCode2 }, { id: "config", label: "Config", icon: SlidersHorizontal }, { id: "deployments", label: "History", icon: Archive }, { id: "output", label: "Output", icon: Terminal }] as const).map(({ id, label, icon: Icon }) => <button key={id} onClick={() => setTab(id)} className={cn("flex items-center gap-2 rounded-md px-3 py-2 font-mono text-[10px] font-bold uppercase tracking-[0.12em] transition", tab === id ? "bg-[#26332f] text-white" : "text-[#80867f] hover:bg-[#efebe2] hover:text-[#35413e]")}><Icon className="size-3.5" />{label}</button>)}</div>
            <p className="font-mono text-[9px] uppercase tracking-[0.12em] text-[#a1a49e]">{tab === "deployments" ? "revision ledger" : "bundle / latest"}</p>
          </header>
          {tab === "files" && <WorkerFileViewer files={files} selectedFile={selectedFile} onSelect={setSelectedFile} />}
          {tab === "config" && <WorkerConfig detail={detail} />}
          {tab === "deployments" && <WorkerDeployments deployments={deployments} />}
          {tab === "output" && <WorkerOutput lines={output} />}
        </section>

        <div className="space-y-6">
          <Panel title="Worker traffic" eyebrow={traffic.available ? "Last 60 minutes" : "Prometheus unavailable"}><MiniTrafficChart values={traffic.traffic} /></Panel>
          <Panel title="Response codes" eyebrow="5 minute rate">
            <StatusCodeMix values={traffic.status_codes} />
          </Panel>
        </div>
      </div>
    </>
  );
}

function WorkerFileViewer({ files, selectedFile, onSelect }: { files: WorkerFile[]; selectedFile?: WorkerFile; onSelect: (file: WorkerFile) => void }) {
  if (!selectedFile) return <WorkerDetailEmpty icon={<FileCode2 />} title="No deployed bundle" copy="Deploy this worker to inspect its bundle file." />;
  return <div className="grid min-h-[510px] md:grid-cols-[190px_1fr]"><aside className="border-b border-[#e7e1d6] bg-[#f5f1e9]/75 py-3 md:border-b-0 md:border-r"><p className="px-4 pb-2 font-mono text-[9px] uppercase tracking-[0.15em] text-[#a0a39c]">Deployed bundle</p><div className="flex items-center gap-2 px-4 py-1.5 font-mono text-[10px] font-bold text-[#68716c]"><Folder className="size-3.5 text-[#d75a41]" />active</div>{files.map((file) => <button key={file.path} onClick={() => onSelect(file)} className={cn("flex w-full items-center gap-2 px-4 py-2 pl-8 text-left font-mono text-[10px] transition", selectedFile.path === file.path ? "bg-[#e5e0d6] font-bold text-[#35413e]" : "text-[#848a83] hover:bg-white/60 hover:text-[#4c5853]")}>{file.name.endsWith(".json") ? <FileJson className="size-3.5 text-[#bd7e35]" /> : <FileCode2 className="size-3.5 text-[#668e7a]" />}{file.name}</button>)}</aside><div className="min-w-0 bg-[#202b29] text-[#d8dfd8]"><div className="flex items-center justify-between border-b border-white/10 px-4 py-3"><p className="font-mono text-[10px] text-[#b5c1bb]">{selectedFile.path}</p><span className="font-mono text-[9px] uppercase tracking-[0.12em] text-[#778781]">{formatBytes(selectedFile.size)} / read only</span></div><pre className="overflow-x-auto p-4 font-mono text-[11px] leading-6"><code>{selectedFile.content.split("\n").map((line, index) => <span key={`${line}-${index}`} className="block"><span className="mr-5 inline-block w-5 select-none text-right text-[#61706b]">{index + 1}</span>{line || " "}</span>)}</code></pre></div></div>;
}

function WorkerConfig({ detail }: { detail?: WorkerDetailData }) {
  if (!detail?.deployment) return <WorkerDetailEmpty icon={<SlidersHorizontal />} title="No runtime config" copy="Deploy this worker to generate its active workerd configuration." />;
  const rows = [["Worker ID", detail.app.id], ["Name", detail.app.name], ["Hostname", detail.app.hostname], ["Deployment", detail.deployment.id], ["Compatibility date", detail.deployment.compatibility_date], ["Entrypoint", detail.deployment.entrypoint], ["Deployed", new Date(detail.deployment.created_at).toLocaleString()]];
  return <div className="p-5"><div className="mb-5 flex items-center gap-3 rounded-lg border border-[#dce2d9] bg-[#eef4ed] px-4 py-3 text-xs font-bold text-[#4d7057]"><Check className="size-4" />Configuration loaded from the active deployment.</div><div className="overflow-hidden rounded-lg border border-[#e2ddd2]">{rows.map(([label, value]) => <div key={label} className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]"><span className="font-mono text-[10px] uppercase tracking-[0.1em] text-[#93978f]">{label}</span><span className="font-mono text-[11px] font-bold text-[#4f5a55]">{value}</span></div>)}</div></div>;
}

function WorkerDeployments({ deployments }: { deployments: ConsoleDeployment[] }) {
  if (!deployments.length) return <WorkerDetailEmpty icon={<Archive />} title="No deployment history" copy="This worker has no recorded revisions yet." />;
  return <div className="min-h-[510px] overflow-x-auto"><table className="w-full min-w-[700px] text-left"><thead><tr className="border-b border-[#e3ded3] font-mono text-[9px] uppercase tracking-[0.14em] text-[#989b95]"><th className="px-5 py-3">State</th><th>Deployment</th><th>Entrypoint</th><th>Bundle</th><th>Compatibility</th><th className="pr-5">Created</th></tr></thead><tbody>{deployments.map((deployment) => <tr key={deployment.id} className="border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/70"><td className="px-5 py-4"><Badge tone={deployment.state === "active" ? "green" : "orange"}>{deployment.state}</Badge></td><td className="max-w-52 truncate font-mono text-[10px] text-[#727a74]" title={deployment.id}>{deployment.id}</td><td className="font-mono text-[10px] text-[#727a74]">{deployment.entrypoint}</td><td className="font-mono text-[10px] text-[#727a74]">{formatBytes(deployment.bundle_size)}</td><td className="font-mono text-[10px] text-[#727a74]">{deployment.compatibility_date}</td><td className="pr-5 text-[#7d837d]">{new Date(deployment.created_at).toLocaleString()}</td></tr>)}</tbody></table></div>;
}

function WorkerOutput({ lines }: { lines: WorkerOutputLine[] }) {
  return <div className="min-h-[510px] bg-[#202b29] p-4"><div className="mb-4 flex items-center gap-2 font-mono text-[9px] uppercase tracking-[0.14em] text-[#82928c]"><span className="size-1.5 rounded-full bg-[#78b88b]" />Shared workerd process output</div>{lines.length ? <div className="space-y-1.5">{lines.map(({ timestamp, level, message }, index) => <p key={`${timestamp}-${index}`} className="font-mono text-[11px] leading-5 text-[#c6d0cb]"><span className="mr-3 text-[#71817b]">{new Date(timestamp).toLocaleTimeString()}</span><span className={cn("mr-3", level === "error" ? "text-[#e87962]" : level === "warn" ? "text-[#e3a65a]" : "text-[#78b88b]")}>{level.toUpperCase()}</span>{message}</p>)}</div> : <p className="pt-16 text-center font-mono text-[10px] uppercase tracking-[0.12em] text-[#71817b]">No runtime output captured yet</p>}</div>;
}

function MiniTrafficChart({ values }: { values: number[] }) {
  const max = Math.max(...values, 0.01);
  if (!values.length) return <EmptyMetrics />;
  return <><div className="flex h-32 items-end gap-1">{values.map((value, index) => <div key={index} title={`${value.toFixed(2)} requests/s`} className="flex-1 rounded-t-sm bg-[#bfd0c6] transition hover:bg-[#e25b3f]" style={{ height: `${Math.max((value / max) * 100, 2)}%` }} />)}</div><div className="mt-3 flex justify-between font-mono text-[9px] text-[#9ba09a]"><span>60 MIN AGO</span><span>NOW</span></div></>;
}

function WorkerDetailEmpty({ icon, title, copy }: { icon: ReactNode; title: string; copy: string }) {
  return <div className="grid min-h-[510px] place-items-center bg-white/30 text-center"><div className="[&_svg]:mx-auto [&_svg]:size-5 [&_svg]:text-[#b7b4ac]">{icon}<p className="mt-3 text-xs font-extrabold text-[#777e78]">{title}</p><p className="mt-1 font-mono text-[9px] uppercase tracking-[0.08em] text-[#a1a49e]">{copy}</p></div></div>;
}

async function fetchJSON<T>(path: string) {
  const response = await fetch(path);
  if (!response.ok) throw new Error(`Request failed: ${response.status}`);
  return response.json() as Promise<T>;
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  return `${(value / 1024).toFixed(1)} KB`;
}

function Pages({ pages, setPages, workers, openDialog, notify }: { pages: Page[]; setPages: (pages: Page[]) => void; workers: Worker[]; openDialog: () => void; notify: (text: string) => void }) {
  return (
    <>
      <PageHeading eyebrow="Routing" title="Pages" copy="Map public paths to workers and keep published routes in view." actions={<Button onClick={openDialog}><Plus className="size-4" />New page</Button>} />
      <Panel title={`${pages.length} routes`} eyebrow="Published surfaces" flush>
        <div className="divide-y divide-[#e5dfd4]">
          {pages.map((page) => (
            <div key={page.id} className="group flex flex-col gap-3 px-5 py-4 transition hover:bg-white/55 sm:flex-row sm:items-center">
              <div className="grid size-10 place-items-center rounded-lg border border-[#ded9ce] bg-white/75 text-[#d75a41]"><FileCode2 className="size-4" /></div>
              <div className="min-w-0 flex-1"><div className="flex items-center gap-2"><p className="text-sm font-extrabold">{page.name}</p><Badge tone={page.status === "published" ? "green" : "orange"}>{page.status}</Badge></div><p className="mt-1 font-mono text-[10px] text-[#8d928c]">{workers.find(({ id }) => id === page.worker)?.name ?? page.worker} → {page.path}</p></div>
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

const emptyMonitoring: MonitoringData = {
  available: false,
  requestsPerSecond: 0,
  p95Latency: 0,
  errorRate: 0,
  openConnections: 0,
  traffic: [],
  statusCodes: [],
};

async function prometheusQuery(query: string) {
  const response = await fetch(`/prometheus/api/v1/query?${new URLSearchParams({ query })}`);
  if (!response.ok) throw new Error("Prometheus unavailable");
  const payload = await response.json() as PrometheusResponse;
  if (payload.status !== "success") throw new Error("Prometheus query failed");
  return payload.data?.result ?? [];
}

async function prometheusRangeQuery(query: string) {
  const end = Math.floor(Date.now() / 1000);
  const start = end - 60 * 60;
  const response = await fetch(`/prometheus/api/v1/query_range?${new URLSearchParams({ query, start: String(start), end: String(end), step: "120" })}`);
  if (!response.ok) throw new Error("Prometheus unavailable");
  const payload = await response.json() as PrometheusResponse;
  if (payload.status !== "success") throw new Error("Prometheus query failed");
  return payload.data?.result ?? [];
}

function resultNumber(result: PrometheusResult[]) {
  return Number(result[0]?.value?.[1] ?? 0) || 0;
}

function Monitoring() {
  const [metrics, setMetrics] = useState<MonitoringData>(emptyMonitoring);
  const [loading, setLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<Date>();

  async function refresh() {
    setLoading(true);
    try {
      const [up, requests, latency, errors, connections, traffic, statusCodes] = await Promise.all([
        prometheusQuery('up{job="traefik"}'),
        prometheusQuery("sum(rate(traefik_entrypoint_requests_total[5m]))"),
        prometheusQuery("histogram_quantile(0.95, sum by (le) (rate(traefik_entrypoint_request_duration_seconds_bucket[5m])))"),
        prometheusQuery('sum(rate(traefik_entrypoint_requests_total{code=~"5.."}[5m]))'),
        prometheusQuery("sum(traefik_entrypoint_open_connections)"),
        prometheusRangeQuery("sum(rate(traefik_entrypoint_requests_total[5m]))"),
        prometheusQuery("sum by (code) (rate(traefik_entrypoint_requests_total[5m]))"),
      ]);
      const requestsPerSecond = resultNumber(requests);
      setMetrics({
        available: resultNumber(up) === 1,
        requestsPerSecond,
        p95Latency: resultNumber(latency),
        errorRate: requestsPerSecond ? resultNumber(errors) / requestsPerSecond : 0,
        openConnections: resultNumber(connections),
        traffic: traffic[0]?.values?.map(([, value]) => Number(value) || 0) ?? [],
        statusCodes: statusCodes.map(({ metric, value }) => ({ code: metric.code ?? "other", value: Number(value?.[1]) || 0 })).sort((a, b) => a.code.localeCompare(b.code)),
      });
      setUpdatedAt(new Date());
    } catch {
      setMetrics(emptyMonitoring);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
    const interval = window.setInterval(() => void refresh(), 15000);
    return () => window.clearInterval(interval);
  }, []);

  const cards = [
    { label: "Request rate", value: `${metrics.requestsPerSecond.toFixed(2)}/s`, note: "5 minute rolling average", icon: Activity },
    { label: "P95 latency", value: formatDuration(metrics.p95Latency), note: "Across Traefik entrypoints", icon: Timer },
    { label: "Error rate", value: `${(metrics.errorRate * 100).toFixed(2)}%`, note: "HTTP 5xx responses", icon: CircleGauge },
    { label: "Open connections", value: String(metrics.openConnections), note: "Active at the edge", icon: Waypoints },
  ];

  return (
    <>
      <PageHeading eyebrow="Observability" title="Monitoring" copy="Live edge traffic from Traefik, collected locally by Prometheus." actions={<Button variant="ghost" onClick={() => void refresh()} disabled={loading}><RefreshCw className={cn("size-4", loading && "animate-spin")} />Refresh metrics</Button>} />
      <div className="mb-4 flex flex-col justify-between gap-3 rounded-lg border border-[#dcd6ca] bg-[#fbf9f3]/70 px-4 py-3 sm:flex-row sm:items-center">
        <div className="flex items-center gap-2 text-xs font-extrabold text-[#46534f]"><span className={cn("size-2 rounded-full", metrics.available ? "bg-[#52a46a]" : "bg-[#c89247]")} />{metrics.available ? "PROMETHEUS SCRAPE HEALTHY" : "PROMETHEUS UNAVAILABLE"}</div>
        <p className="font-mono text-[9px] uppercase tracking-[0.12em] text-[#989c96]">{updatedAt ? `Updated ${updatedAt.toLocaleTimeString()}` : "Waiting for local metrics"}</p>
      </div>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map(({ label, value, note, icon: Icon }, index) => (
          <div key={label} style={{ animationDelay: `${index * 70}ms` }} className="paper-panel animate-rise rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85 p-5">
            <Icon className="size-4 text-[#d75a41]" />
            <p className="mt-6 font-display text-4xl tracking-[-0.05em] text-[#26332f]">{value}</p>
            <p className="mt-2 text-xs font-extrabold">{label}</p>
            <p className="mt-1 font-mono text-[9px] uppercase tracking-[0.08em] text-[#999d97]">{note}</p>
          </div>
        ))}
      </div>
      <div className="mt-6 grid gap-6 lg:grid-cols-[1.5fr_1fr]">
        <Panel title="Requests per second" eyebrow="Last 60 minutes">
          <TrafficChart values={metrics.traffic} />
        </Panel>
        <Panel title="Response codes" eyebrow="5 minute rate">
          <StatusCodeMix values={metrics.statusCodes} />
        </Panel>
      </div>
    </>
  );
}

function formatDuration(seconds: number) {
  return seconds < 1 ? `${Math.round(seconds * 1000)}ms` : `${seconds.toFixed(2)}s`;
}

function TrafficChart({ values }: { values: number[] }) {
  const max = Math.max(...values, 0.01);
  if (!values.length) return <EmptyMetrics />;
  return (
    <>
      <div className="flex h-52 items-end gap-1 pt-7">
        {values.map((value, index) => <div key={index} title={`${value.toFixed(2)} requests/s`} className="group flex-1 rounded-t-sm bg-[#cbd9d1] transition hover:bg-[#e25b3f]" style={{ height: `${Math.max((value / max) * 100, 2)}%` }} />)}
      </div>
      <div className="mt-3 flex justify-between font-mono text-[9px] text-[#9ba09a]"><span>60 MIN AGO</span><span>30 MIN AGO</span><span>NOW</span></div>
    </>
  );
}

function StatusCodeMix({ values }: { values: { code: string; value: number }[] }) {
  const total = values.reduce((sum, { value }) => sum + value, 0);
  if (!values.length) return <EmptyMetrics />;
  return <div className="space-y-4">{values.map(({ code, value }) => <div key={code}><div className="mb-1.5 flex justify-between font-mono text-[10px]"><span className="font-bold text-[#58645f]">HTTP {code}</span><span className="text-[#989c96]">{value.toFixed(2)}/s</span></div><div className="h-2 overflow-hidden rounded-full bg-[#e6e3db]"><div className={cn("h-full rounded-full", code.startsWith("5") ? "bg-[#d75a41]" : code.startsWith("4") ? "bg-[#c89247]" : "bg-[#6d9c79]")} style={{ width: `${total ? Math.max((value / total) * 100, 2) : 0}%` }} /></div></div>)}</div>;
}

function EmptyMetrics() {
  return <div className="grid h-52 place-items-center rounded-lg border border-dashed border-[#d8d2c7] bg-white/30 text-center"><div><BarChart3 className="mx-auto size-5 text-[#b7b4ac]" /><p className="mt-3 text-xs font-extrabold text-[#777e78]">No traffic samples yet</p><p className="mt-1 font-mono text-[9px] uppercase tracking-[0.08em] text-[#a1a49e]">Start the stack or send a request through Traefik</p></div></div>;
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
  const [hostname, setHostname] = useState("");
  const [name, setName] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    let worker: Worker = { id: crypto.randomUUID().replace(/-/g, ""), name, hostname, created_at: new Date().toISOString(), status: "draft", requests: "0", deployment: "awaiting deploy" };
    if (apiConnected) {
      const response = await fetch("/v1/apps", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ name, hostname }) });
      if (!response.ok) return notify("Worker registration failed");
      worker = { ...worker, ...await response.json() as Worker };
    }
    setWorkers([...workers, worker]); setName(""); setHostname(""); onClose(); notify(`${worker.name} registered`);
  }
  return <Dialog open={open} onClose={onClose} title="Register worker" description="Create an isolated runtime target. You can deploy a worker bundle after registration."><form className="space-y-4" onSubmit={submit}><Field label="Name"><Input required placeholder="Analytics worker" value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label="Hostname"><Input required placeholder="analytics.acme.internal" value={hostname} onChange={(event) => setHostname(event.target.value)} /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Register worker</Button></div></form></Dialog>;
}

function CreatePageDialog({ open, onClose, setPages, workers, notify }: { open: boolean; onClose: () => void; setPages: React.Dispatch<React.SetStateAction<Page[]>>; workers: Worker[]; notify: (text: string) => void }) {
  const [name, setName] = useState(""); const [path, setPath] = useState("/"); const [workerID, setWorkerID] = useState("");
  const selectedWorkerID = workerID || workers[0]?.id || "";
  function submit(event: FormEvent) { event.preventDefault(); setPages((pages) => [...pages, { id: Date.now(), name, path, worker: selectedWorkerID || "unassigned", updated: "Just now", status: "draft" }]); onClose(); notify(`${name} route created`); setName(""); setPath("/"); setWorkerID(""); }
  return <Dialog open={open} onClose={onClose} title="Add page route" description="Create a route mapping. New surfaces start as drafts until published."><form className="space-y-4" onSubmit={submit}><Field label="Page name"><Input required placeholder="Team dashboard" value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label="Public path"><Input required placeholder="/dashboard" value={path} onChange={(event) => setPath(event.target.value)} /></Field><Field label="Worker"><select required value={selectedWorkerID} onChange={(event) => setWorkerID(event.target.value)} className="h-10 w-full rounded-md border border-[#d6d0c3] bg-white/80 px-3 text-sm outline-none">{workers.map((worker) => <option key={worker.id} value={worker.id}>{worker.name}</option>)}</select></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Create route</Button></div></form></Dialog>;
}

function UploadDialog({ open, onClose, setObjects, notify }: { open: boolean; onClose: () => void; setObjects: React.Dispatch<React.SetStateAction<StoredObject[]>>; notify: (text: string) => void }) {
  const [path, setPath] = useState("");
  function submit(event: FormEvent) { event.preventDefault(); setObjects((objects) => [{ id: Date.now(), name: path, type: path.split(".").pop()?.toUpperCase() ?? "FILE", size: "0 KB", updated: "Just now" }, ...objects]); onClose(); notify(`${path} uploaded`); setPath(""); }
  return <Dialog open={open} onClose={onClose} title="Upload object" description="Add an object path to the shared MinIO-backed asset bucket."><form className="space-y-4" onSubmit={submit}><div className="grid place-items-center rounded-lg border border-dashed border-[#c9c2b6] bg-white/45 px-4 py-7 text-center"><FolderOpen className="size-7 text-[#d35c45]" /><p className="mt-3 text-xs font-bold">Choose a destination path</p><p className="mt-1 font-mono text-[9px] text-[#999c96]">OBJECT CONTENT UPLOAD WILL USE A PRESIGNED URL</p></div><Field label="Object path"><Input required placeholder="uploads/report.csv" value={path} onChange={(event) => setPath(event.target.value)} /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Upload object</Button></div></form></Dialog>;
}
