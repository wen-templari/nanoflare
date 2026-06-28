import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import {
  Activity, Archive, ArrowLeft, ArrowUpRight, BarChart3, Boxes, Check,
  ChevronDown, ChevronRight, CircleGauge, CloudUpload, Code2, Copy,
  FileCode2, FileJson, Folder, GitBranch, Globe2, KeyRound, MoreHorizontal,
  Plus, RefreshCw, Save, Search, Server, Settings, SlidersHorizontal, Terminal,
  Timer, Trash2, Waypoints,
} from "lucide-react";
import { Badge } from "./components/ui/badge";
import { Button } from "./components/ui/button";
import { Dialog } from "./components/ui/dialog";
import { Input } from "./components/ui/input";
import { cn } from "./lib/utils";

type Section = "overview" | "workers" | "monitoring";
type WorkerAuth = { protected_routes?: string[] };
type Worker = { id: string; name: string; hostname: string; created_at: string; auth?: WorkerAuth; status?: "live" | "draft"; requests?: string; deployment?: string };
type WorkerDetailTab = "files" | "kv" | "config" | "deployments" | "output";
type WorkerDeployment = { id: string; entrypoint: string; bundle_size: number; compatibility_date: string; created_at: string };
type WorkerDetailData = { app: Worker; deployment?: WorkerDeployment };
type ConsoleDeployment = { id: string; app_id: string; app_name: string; hostname: string; entrypoint: string; bundle_size: number; compatibility_date: string; state: "active" | "inactive"; created_at: string };
type WorkerFile = { name: string; path: string; size: number; content: string };
type WorkerOutputLine = { timestamp: string; level: string; message: string };
type WorkerKVKey = { key: string; size: number };
type WorkerTraffic = {
  available: boolean;
  requests_per_second: number;
  p95_latency: number;
  error_rate: number;
  traffic: number[];
  status_codes: { code: string; value: number }[];
};
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

const navItems: { section: Section; label: string; icon: typeof Server }[] = [
  { section: "overview", label: "Overview", icon: CircleGauge },
  { section: "workers", label: "Workers", icon: Waypoints },
  { section: "monitoring", label: "Monitoring", icon: BarChart3 },
];

export function App() {
  const [section, setSection] = useState<Section>("overview");
  const [workers, setWorkers] = useState<Worker[]>(demoWorkers);
  const [workerDialog, setWorkerDialog] = useState(false);
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
          {section === "overview" && <Overview workers={workers} setSection={setSection} />}
          {section === "workers" && <Workers workers={workers} setWorkers={setWorkers} openDialog={() => setWorkerDialog(true)} notify={notify} apiConnected={apiConnected} />}
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

function Overview({ workers, setSection }: { workers: Worker[]; setSection: (section: Section) => void }) {
  const stats = [
    { label: "Workers", value: workers.length, note: `${workers.filter((worker) => worker.status === "live").length} live · ${workers.filter((worker) => worker.status === "draft").length} draft`, icon: Waypoints, section: "workers" as Section },
    { label: "Monitoring", value: workers.filter((worker) => worker.status === "live").length, note: "workers with active deployments", icon: BarChart3, section: "monitoring" as Section },
  ];
  return (
    <>
      <PageHeading eyebrow="Sunday, 31 May" title="Good afternoon, Clas." copy="Your private runtime is steady. Here is the shape of your workspace today." />
      <div className="grid gap-4 md:grid-cols-2">
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
          <Event icon={<CloudUpload />} text="worker bundle deployed" time="34m" />
          <Event icon={<KeyRound />} text="env.KV binding refreshed" time="2h" />
          <Event icon={<Code2 />} text="billing-sync deployed" time="5h" />
          <Event icon={<Archive />} text="previous generation retired" time="8h" />
        </Panel>
      </div>
    </>
  );
}

function Workers({ workers, setWorkers, openDialog, notify, apiConnected }: { workers: Worker[]; setWorkers: (workers: Worker[]) => void; openDialog: () => void; notify: (text: string) => void; apiConnected: boolean }) {
  const [selectedWorker, setSelectedWorker] = useState<Worker>();
  if (selectedWorker) {
    return <WorkerDetail worker={selectedWorker} onBack={() => setSelectedWorker(undefined)} notify={notify} apiConnected={apiConnected} />;
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

function WorkerDetail({ worker, onBack, notify, apiConnected }: { worker: Worker; onBack: () => void; notify: (text: string) => void; apiConnected: boolean }) {
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
      if (!apiConnected) {
        const demoDeployment = demoDeployments.find((deployment) => deployment.app_id === worker.id);
        setDetail({
          app: worker,
          deployment: demoDeployment && {
            id: demoDeployment.id,
            entrypoint: demoDeployment.entrypoint,
            bundle_size: demoDeployment.bundle_size,
            compatibility_date: demoDeployment.compatibility_date,
            created_at: demoDeployment.created_at,
          },
        });
        setFiles([]);
        setDeployments(demoDeployments.filter((deployment) => deployment.app_id === worker.id));
        setOutput([]);
        setTraffic({ available: false, requests_per_second: 0, p95_latency: 0, error_rate: 0, traffic: [], status_codes: [] });
        setError("");
        setLoading(false);
        return;
      }
      const [nextDetail, nextFiles, nextDeployments, nextOutput, nextTraffic] = await Promise.all([
        fetchJSON<WorkerDetailData>(`/v1/apps/${worker.id}`).catch(() => undefined),
        fetchJSON<WorkerFile[]>(`/v1/apps/${worker.id}/files`).catch(() => []),
        fetchJSON<ConsoleDeployment[]>(`/v1/apps/${worker.id}/deployments`).catch(() => []),
        fetchJSON<WorkerOutputLine[]>(`/v1/apps/${worker.id}/output`).catch(() => []),
        fetchJSON<WorkerTraffic>(`/v1/apps/${worker.id}/traffic`).catch(() => ({ available: false, requests_per_second: 0, p95_latency: 0, error_rate: 0, traffic: [], status_codes: [] })),
      ]);
      if (cancelled) return;
      setDetail(nextDetail);
      setFiles(nextFiles);
      setDeployments(nextDeployments);
      setOutput(nextOutput);
      setTraffic(nextTraffic);
      setError(nextDetail ? "" : "Worker detail API unavailable");
      setSelectedFile((current) => nextFiles.find((file) => file.path === current?.path) ?? nextFiles[0]);
      setLoading(false);
    }
    void refresh();
    const interval = window.setInterval(() => void refresh(), 15000);
    return () => { cancelled = true; window.clearInterval(interval); };
  }, [apiConnected, worker]);

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
            <div className="flex gap-1">{([{ id: "files", label: "Files", icon: FileCode2 }, { id: "kv", label: "KV", icon: KeyRound }, { id: "config", label: "Config", icon: SlidersHorizontal }, { id: "deployments", label: "History", icon: Archive }, { id: "output", label: "Output", icon: Terminal }] as const).map(({ id, label, icon: Icon }) => <button key={id} onClick={() => setTab(id)} className={cn("flex items-center gap-2 rounded-md px-3 py-2 font-mono text-[10px] font-bold uppercase tracking-[0.12em] transition", tab === id ? "bg-[#26332f] text-white" : "text-[#80867f] hover:bg-[#efebe2] hover:text-[#35413e]")}><Icon className="size-3.5" />{label}</button>)}</div>
            <p className="font-mono text-[9px] uppercase tracking-[0.12em] text-[#a1a49e]">{tab === "deployments" ? "revision ledger" : tab === "kv" ? "key lookup" : "bundle / latest"}</p>
          </header>
          {tab === "files" && <WorkerFileViewer files={files} selectedFile={selectedFile} onSelect={setSelectedFile} />}
          {tab === "kv" && <WorkerKV workerID={worker.id} notify={notify} />}
          {tab === "config" && <WorkerConfig detail={detail} apiConnected={apiConnected} notify={notify} />}
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

function WorkerConfig({ detail, apiConnected, notify }: { detail?: WorkerDetailData; apiConnected: boolean; notify: (text: string) => void }) {
  const [protectedRoutes, setProtectedRoutes] = useState((detail?.app.auth?.protected_routes ?? []).join("\n"));
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setProtectedRoutes((detail?.app.auth?.protected_routes ?? []).join("\n"));
  }, [detail?.app.id, detail?.app.auth?.protected_routes]);

  if (!detail?.deployment) return <WorkerDetailEmpty icon={<SlidersHorizontal />} title="No runtime config" copy="Deploy this worker to generate its active workerd configuration." />;
  const appID = detail.app.id;
  const rows = [["Worker ID", detail.app.id], ["Name", detail.app.name], ["Hostname", detail.app.hostname], ["Deployment", detail.deployment.id], ["Compatibility date", detail.deployment.compatibility_date], ["Entrypoint", detail.deployment.entrypoint], ["Deployed", new Date(detail.deployment.created_at).toLocaleString()]];

  async function saveRoutes() {
    if (!apiConnected) {
      notify("Protected routes are only editable when platformd is connected");
      return;
    }
    setSaving(true);
    try {
      const protected_routes = protectedRoutes.split("\n").map((route) => route.trim()).filter(Boolean);
      const response = await fetch(`/v1/apps/${appID}`, {
        method: "PATCH",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ auth: { protected_routes } }),
      });
      if (!response.ok) throw new Error(`Config update failed (${response.status})`);
      notify("Protected routes updated");
    } catch (error) {
      notify(error instanceof Error ? error.message : "Config update failed");
    } finally {
      setSaving(false);
    }
  }

  return <div className="p-5"><div className="mb-5 flex items-center gap-3 rounded-lg border border-[#dce2d9] bg-[#eef4ed] px-4 py-3 text-xs font-bold text-[#4d7057]"><Check className="size-4" />Configuration loaded from the active deployment.</div><div className="overflow-hidden rounded-lg border border-[#e2ddd2]">{rows.map(([label, value]) => <div key={label} className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]"><span className="font-mono text-[10px] uppercase tracking-[0.1em] text-[#93978f]">{label}</span><span className="font-mono text-[11px] font-bold text-[#4f5a55]">{value}</span></div>)}</div><div className="mt-5 rounded-lg border border-[#e2ddd2] bg-white/50 p-4"><div className="mb-2 flex items-center justify-between"><p className="font-mono text-[10px] uppercase tracking-[0.12em] text-[#7f857e]">Protected routes</p><Button type="button" onClick={() => void saveRoutes()} disabled={saving}><Save className="size-3.5" />Save routes</Button></div><p className="mb-3 text-xs text-[#6f766f]">One absolute path per line. Example: <span className="font-mono">/admin/*</span></p><textarea value={protectedRoutes} onChange={(event) => setProtectedRoutes(event.target.value)} spellCheck={false} className="min-h-40 w-full rounded-md border border-[#d6d0c3] bg-[#fdfbf6] p-3 font-mono text-[11px] leading-6 text-[#35413e] outline-none" placeholder="/admin/*&#10;/api/private/*" /></div></div>;
}

function WorkerKV({ workerID, notify }: { workerID: string; notify: (text: string) => void }) {
  const [keys, setKeys] = useState<WorkerKVKey[]>([]);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const path = key.trim() ? `/v1/apps/${workerID}/kv/${encodeURIComponent(key.trim())}` : "";

  async function refreshKeys() {
    setLoading(true); setStatus("");
    try {
      setKeys(await fetchJSON<WorkerKVKey[]>(`/v1/apps/${workerID}/kv`));
      setStatus("Keys refreshed");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV list failed");
    } finally {
      setLoading(false);
    }
  }

  async function readKey(nextKey = key.trim()) {
    if (!nextKey) return;
    setLoading(true); setStatus("");
    try {
      setKey(nextKey);
      const response = await fetch(`/v1/apps/${workerID}/kv/${encodeURIComponent(nextKey)}`);
      if (response.status === 404) {
        setValue(""); setStatus("Key not found");
        return;
      }
      if (!response.ok) throw new Error(`KV read failed (${response.status})`);
      setValue(await response.text());
      setStatus("Value loaded");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV read failed");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refreshKeys();
  }, [workerID]);

  async function writeKey() {
    if (!path) return;
    setLoading(true); setStatus("");
    try {
      const response = await fetch(path, { method: "PUT", body: value });
      if (!response.ok) throw new Error(`KV write failed (${response.status})`);
      setStatus("Value saved");
      setKeys(await fetchJSON<WorkerKVKey[]>(`/v1/apps/${workerID}/kv`));
      notify(`${key.trim()} saved`);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV write failed");
    } finally {
      setLoading(false);
    }
  }

  async function deleteKey() {
    if (!path) return;
    setLoading(true); setStatus("");
    try {
      const response = await fetch(path, { method: "DELETE" });
      if (!response.ok) throw new Error(`KV delete failed (${response.status})`);
      setValue("");
      setStatus("Key deleted");
      setKeys(await fetchJSON<WorkerKVKey[]>(`/v1/apps/${workerID}/kv`));
      notify(`${key.trim()} deleted`);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV delete failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="grid min-h-[510px] gap-0 md:grid-cols-[260px_1fr]">
      <aside className="border-b border-[#e7e1d6] bg-[#f5f1e9]/75 py-3 md:border-b-0 md:border-r">
        <div className="flex items-center justify-between px-4 pb-2">
          <p className="font-mono text-[9px] uppercase tracking-[0.15em] text-[#a0a39c]">KV keys</p>
          <Button type="button" variant="ghost" size="icon" aria-label="Refresh KV keys" onClick={() => void refreshKeys()} disabled={loading}><RefreshCw className={cn("size-3.5", loading && "animate-spin")} /></Button>
        </div>
        <button onClick={() => { setKey(""); setValue(""); setStatus("Ready for a new key"); }} className="flex w-full items-center gap-2 px-4 py-2 text-left font-mono text-[10px] font-bold text-[#68716c] transition hover:bg-white/60"><Plus className="size-3.5 text-[#d75a41]" />new key</button>
        {keys.map((item) => (
          <button key={item.key} onClick={() => void readKey(item.key)} className={cn("flex w-full items-center gap-2 px-4 py-2 text-left font-mono text-[10px] transition", key === item.key ? "bg-[#e5e0d6] font-bold text-[#35413e]" : "text-[#848a83] hover:bg-white/60 hover:text-[#4c5853]")}>
            <KeyRound className="size-3.5 text-[#668e7a]" />
            <span className="min-w-0 flex-1 truncate">{item.key}</span>
            <span className="text-[9px] text-[#a0a39c]">{formatBytes(item.size)}</span>
          </button>
        ))}
        {!keys.length && <p className="px-4 py-8 text-center font-mono text-[9px] uppercase tracking-[0.08em] text-[#a1a49e]">No keys yet</p>}
        {status && <p className="mx-4 mt-3 rounded-md border border-[#ded8cd] bg-white/55 px-3 py-2 font-mono text-[10px] uppercase tracking-[0.08em] text-[#727a74]">{status}</p>}
      </aside>
      <div className="p-5">
        <form className="flex flex-col gap-3 sm:flex-row" onSubmit={(event) => { event.preventDefault(); void readKey(); }}>
          <div className="flex min-w-0 flex-1 items-center gap-2 rounded-md border border-[#d6d0c3] bg-white/75 px-3">
            <Search className="size-4 text-[#959a93]" />
            <Input required value={key} onChange={(event) => setKey(event.target.value)} placeholder="visits" className="h-10 border-0 bg-transparent p-0 focus:ring-0" />
          </div>
          <Button type="submit" variant="outline" disabled={loading}><Search className="size-3.5" />Read</Button>
        </form>
        <div className="mt-4 overflow-hidden rounded-lg border border-[#d9d3c7] bg-[#202b29]">
          <div className="flex items-center justify-between border-b border-white/10 px-4 py-3">
            <p className="font-mono text-[10px] text-[#b5c1bb]">{key.trim() || "Select a key"}</p>
            <span className="font-mono text-[9px] uppercase tracking-[0.12em] text-[#778781]">{value.length} bytes</span>
          </div>
          <textarea value={value} onChange={(event) => setValue(event.target.value)} spellCheck={false} className="min-h-80 w-full resize-y bg-transparent p-4 font-mono text-[11px] leading-6 text-[#d8dfd8] outline-none" placeholder="Value" />
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={() => void deleteKey()} disabled={loading || !key.trim()}><Trash2 className="size-3.5" />Delete</Button>
          <Button type="button" onClick={() => void writeKey()} disabled={loading || !key.trim()}><Save className="size-3.5" />Save</Button>
        </div>
      </div>
    </div>
  );
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
  const [protectedRoutes, setProtectedRoutes] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    const auth = { protected_routes: protectedRoutes.split("\n").map((route) => route.trim()).filter(Boolean) };
    let worker: Worker = { id: crypto.randomUUID().replace(/-/g, ""), name, hostname, auth, created_at: new Date().toISOString(), status: "draft", requests: "0", deployment: "awaiting deploy" };
    if (apiConnected) {
      const trimmedHostname = hostname.trim();
      const response = await fetch("/v1/apps", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(trimmedHostname ? { name, hostname: trimmedHostname, auth } : { name, auth }) });
      if (!response.ok) return notify("Worker registration failed");
      worker = { ...worker, ...await response.json() as Worker };
    }
    setWorkers([...workers, worker]); setName(""); setHostname(""); setProtectedRoutes(""); onClose(); notify(`${worker.name} registered`);
  }
  return <Dialog open={open} onClose={onClose} title="Register worker" description="Create an isolated runtime target. You can deploy a worker bundle after registration."><form className="space-y-4" onSubmit={submit}><Field label="Name"><Input required placeholder="Analytics worker" value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label="Hostname"><Input placeholder="analytics.acme.internal" value={hostname} onChange={(event) => setHostname(event.target.value)} /></Field><Field label="Protected routes"><textarea value={protectedRoutes} onChange={(event) => setProtectedRoutes(event.target.value)} spellCheck={false} className="min-h-28 w-full rounded-md border border-[#d6d0c3] bg-[#fdfbf6] p-3 font-mono text-[11px] leading-6 text-[#35413e] outline-none" placeholder="/admin/*&#10;/api/private/*" /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Register worker</Button></div></form></Dialog>;
}
