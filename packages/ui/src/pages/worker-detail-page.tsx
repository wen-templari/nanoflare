import {
  Banner,
  Button,
  ChartPalette,
  Dialog,
  DropdownMenu,
  Input,
  LayerCard,
  Link,
  Table,
  Tabs,
  Text,
  TimeseriesChart,
  Tooltip,
} from "@cloudflare/kumo";
import { lazy, Suspense, useEffect, useState, type ReactNode } from "react";
import {
  Activity,
  AlertTriangle,
  Archive,
  ArrowDown,
  Copy,
  FileCode2,
  FileJson,
  Folder,
  Gauge,
  GitBranch,
  Globe2,
  MoreHorizontal,
  RefreshCw,
  Save,
  SlidersHorizontal,
  Terminal,
  TimerReset,
  X,
} from "lucide-react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { apiFetch, errorText, fetchJSON } from "../app/api";
import { useQueryTab } from "../app/use-query-tab";
import { useWorkspace } from "../app/workspace-context";
import type {
  ConsoleDeployment,
  WorkerDeployment,
  WorkerDetailData,
  WorkerDetailTab,
  WorkerFile,
  WorkerOutputLine,
  WorkerTraffic,
} from "../app/types";
import { formatBytes } from "../app/utils";
import {
  EmptyMetrics,
  Panel,
  StatusCodeMix,
  WorkerDetailEmpty,
} from "../components/shared/primitives";
import { cn } from "../lib/utils";
import { echarts } from "../lib/kumo-echarts";

const emptyTraffic: WorkerTraffic = {
  available: false,
  requests_per_second: 0,
  p95_latency: 0,
  error_rate: 0,
  invocations: 0,
  errors: 0,
  bundle_size: 0,
  traffic: [],
  duration_ms_avg: 0,
  duration_ms_p95: 0,
  duration_ms_per_second: 0,
  duration_series: [],
  status_codes: [],
};

function normalizeTraffic(input?: Partial<WorkerTraffic> | null): WorkerTraffic {
  return {
    ...emptyTraffic,
    ...input,
    traffic: Array.isArray(input?.traffic) ? input!.traffic : [],
    duration_series: Array.isArray(input?.duration_series) ? input!.duration_series : [],
    status_codes: Array.isArray(input?.status_codes) ? input!.status_codes : [],
  };
}

const WorkerDefinitionFlow = lazy(() =>
  import("../components/worker-definition-flow").then((module) => ({
    default: module.WorkerDefinitionFlow,
  })),
);
const workerDetailTabs = [
  "overview",
  "metrics",
  "deployments",
  "files",
  "output",
  "settings",
] as const;

export function WorkerDetailPage() {
  const { workerId } = useParams();
  const { workers, notify, apiConnected, workspaceReady } = useWorkspace();
  const worker = workers.find((item) => item.id === workerId);

  if (!workspaceReady) return null;
  if (!worker) return <Navigate to="/workers" replace />;

  return <WorkerDetailContent worker={worker} notify={notify} apiConnected={apiConnected} />;
}

function WorkerDetailContent({
  worker,
  notify,
  apiConnected,
}: {
  worker: {
    id: string;
    name: string;
    hostname: string;
    bindings?: WorkerDeployment["bindings"];
    created_at: string;
    created_by?: string;
  };
  notify: (text: string) => void;
  apiConnected: boolean;
}) {
  const navigate = useNavigate();
  const { databases, namespaces } = useWorkspace();
  const [tab, setTab] = useQueryTab<WorkerDetailTab>(workerDetailTabs, "overview");
  const [detail, setDetail] = useState<WorkerDetailData>();
  const [files, setFiles] = useState<WorkerFile[]>([]);
  const [deployments, setDeployments] = useState<ConsoleDeployment[]>([]);
  const [selectedFile, setSelectedFile] = useState<WorkerFile>();
  const [output, setOutput] = useState<WorkerOutputLine[]>([]);
  const [traffic, setTraffic] = useState<WorkerTraffic>(emptyTraffic);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;

    async function refresh() {
      if (!apiConnected) {
        setDetail(undefined);
        setFiles([]);
        setDeployments([]);
        setOutput([]);
        setTraffic(emptyTraffic);
        setError("Worker detail API unavailable");
        setLoading(false);
        return;
      }

      const [nextDetail, nextFiles, nextDeployments, nextOutput, nextTraffic] = await Promise.all([
        fetchJSON<WorkerDetailData>(`/v1/workers/${worker.id}`).catch(() => undefined),
        fetchJSON<WorkerFile[]>(`/v1/workers/${worker.id}/files`).catch(() => []),
        fetchJSON<ConsoleDeployment[]>(`/v1/workers/${worker.id}/deployments`).catch(() => []),
        fetchJSON<WorkerOutputLine[]>(`/v1/workers/${worker.id}/output`).catch(() => []),
        fetchJSON<WorkerTraffic>(`/v1/workers/${worker.id}/traffic`).catch(() => emptyTraffic),
      ]);
      if (cancelled) return;
      setDetail(nextDetail);
      setFiles(nextFiles);
      setDeployments(nextDeployments);
      setOutput(nextOutput);
      setTraffic(normalizeTraffic(nextTraffic));
      setError(nextDetail ? "" : "Worker detail API unavailable");
      setSelectedFile(
        (current) => nextFiles.find((file) => file.path === current?.path) ?? nextFiles[0],
      );
      setLoading(false);
    }

    void refresh();
    const interval = window.setInterval(() => void refresh(), 15000);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [apiConnected, worker]);

  const deployment = detail?.deployment;
  const recentDeployments = [...deployments]
    .sort(
      (left, right) => new Date(right.created_at).getTime() - new Date(left.created_at).getTime(),
    )
    .slice(0, 5);

  return (
    <>
      {(loading || error) && (
        <Banner className="mb-4" variant={error ? "error" : "default"}>
          {error || "Loading worker detail from nanoflared"}
        </Banner>
      )}

      <div>
        <div className="mb-4">
          <Tabs
            className="inline-flex max-w-full"
            listClassName="max-w-full overflow-x-auto"
            tabs={(
              [
                { id: "overview", label: "Overview", icon: GitBranch },
                { id: "metrics", label: "Metrics", icon: Activity },
                { id: "deployments", label: "Deployments", icon: Archive },
                { id: "files", label: "Files", icon: FileCode2 },
                { id: "output", label: "Output", icon: Terminal },
                { id: "settings", label: "Settings", icon: SlidersHorizontal },
              ] as const
            ).map(({ id, label }) => ({ label, value: id }))}
            onValueChange={(value) => setTab(value as WorkerDetailTab)}
            value={tab}
            variant="segmented"
          />
        </div>
        {tab === "overview" && (
          <WorkerOverview
            worker={detail?.app ?? worker}
            deployment={detail?.deployment}
            databases={databases}
            namespaces={namespaces}
            onOpenNamespace={(namespaceID) => navigate(`/kv/${namespaceID}`)}
            onOpenDatabase={(databaseID) => navigate(`/databases/${databaseID}`)}
            onOpenBucket={(bucketID) => navigate(`/object-storage/${bucketID}`)}
            onOpenDeployments={() => setTab("deployments")}
            recentDeployments={recentDeployments}
            traffic={traffic}
          />
        )}
        {tab === "metrics" && <WorkerMetrics traffic={traffic} />}
        {tab === "deployments" && (
          <WorkerDeployments
            deployments={deployments}
            notify={notify}
            onSaved={setDeployments}
            traffic={traffic}
            workerID={worker.id}
          />
        )}
        {tab === "files" && (
          <WorkerFileViewer files={files} selectedFile={selectedFile} onSelect={setSelectedFile} />
        )}
        {tab === "output" && <WorkerOutput workerID={worker.id} deployments={deployments} initialLines={output} />}
        {tab === "settings" && (
          <WorkerConfig
            detail={detail}
            worker={worker}
            apiConnected={apiConnected}
            notify={notify}
          />
        )}
      </div>
    </>
  );
}

function WorkerMetrics({ traffic }: { traffic: WorkerTraffic }) {
  return (
    <div className="grid gap-6 xl:grid-cols-2">
      <Panel
        title="Worker traffic"
        eyebrow={traffic.available ? "Last 24 hours" : "Prometheus unavailable"}
      >
        <MiniTrafficChart values={traffic.traffic} />
      </Panel>
      <Panel
        title="Handler duration"
        eyebrow={traffic.duration_series.length ? "Last 24 hours" : "Waiting for runtime timings"}
      >
        <MiniTrafficChart values={traffic.duration_series} />
      </Panel>
      <Panel title="Response codes" eyebrow="5 minute rate">
        <StatusCodeMix values={traffic.status_codes} />
      </Panel>
      <LayerCard>
        <LayerCard.Secondary>
          <Text as="h2" variant="heading3">
            Current values
          </Text>
        </LayerCard.Secondary>
        <LayerCard.Primary className="grid grid-cols-2 divide-x divide-y divide-kumo-line sm:grid-cols-3">
          {[
            ["Requests/sec", traffic.requests_per_second.toFixed(2)],
            ["Error rate", `${traffic.error_rate.toFixed(2)}%`],
            ["Handler average", formatMilliseconds(traffic.duration_ms_avg)],
            ["Handler p95", formatMilliseconds(traffic.duration_ms_p95)],
            ["Duration rate", `${traffic.duration_ms_per_second.toFixed(2)} ms/s`],
            ["Invocations", compactNumber(traffic.invocations)],
          ].map(([label, value]) => (
            <div className="px-5 py-4" key={label}>
              <Text size="xs" variant="secondary">
                {label}
              </Text>
              <div className="mt-2">
                <Text as="p" variant="heading3">
                  {value}
                </Text>
              </div>
            </div>
          ))}
        </LayerCard.Primary>
      </LayerCard>
    </div>
  );
}

function WorkerOverview({
  deployment,
  databases,
  namespaces,
  onOpenNamespace,
  onOpenDatabase,
  onOpenBucket,
  onOpenDeployments,
  recentDeployments,
  traffic,
  worker,
}: {
  deployment?: WorkerDeployment;
  databases: { id: string; name: string; created_at: string }[];
  namespaces: { id: string; name: string; created_at: string }[];
  onOpenNamespace: (namespaceID: string) => void;
  onOpenDatabase: (databaseID: string) => void;
  onOpenBucket: (bucketID: string) => void;
  onOpenDeployments: () => void;
  recentDeployments: ConsoleDeployment[];
  traffic: WorkerTraffic;
  worker: WorkerDetailData["app"];
}) {
  const bindings = deployment?.bindings ?? worker.bindings ?? [];
  return (
    <div className="space-y-6">
      <LayerCard className="px-5 py-4">
        <div className="flex items-center justify-between gap-4">
          <div className="flex min-w-0 items-center gap-2">
            <Globe2 className="size-4 shrink-0 text-kumo-subtle" />
            <Link
              className="truncate text-sm"
              href={hostnameHref(worker.hostname)}
              target="_blank"
              title={worker.hostname}
            >
              {worker.hostname}
            </Link>
          </div>
          <div className="flex min-w-0 items-center gap-2">
            <Text size="sm" variant="secondary">
              Active version
            </Text>
            <button
              type="button"
              className="max-w-full truncate font-mono text-sm text-kumo-link hover:underline disabled:cursor-default disabled:text-kumo-subtle disabled:no-underline"
              disabled={!deployment}
              onClick={onOpenDeployments}
              title={deployment?.id}
            >
              {deployment ? shortDeploymentID(deployment.id) : "Awaiting deploy"}
            </button>
          </div>
        </div>
      </LayerCard>

      <LayerCard className="min-h-[360px] overflow-hidden p-3">
        <Suspense
          fallback={
            <div className="h-[420px] animate-pulse rounded-xl border border-gray-200 bg-white" />
          }
        >
          <WorkerDefinitionFlow
            worker={worker}
            deployment={deployment}
            databases={databases}
            namespaces={namespaces}
            onOpenNamespace={onOpenNamespace}
            onOpenDatabase={onOpenDatabase}
            onOpenBucket={onOpenBucket}
          />
        </Suspense>
      </LayerCard>

      <LayerCard>
        <LayerCard.Secondary className="flex items-center gap-2">
          <Text as="h2" variant="heading3">
            Metrics
          </Text>
          <Text size="xs" variant="secondary">
            Last 24 hours
          </Text>
        </LayerCard.Secondary>
        <LayerCard.Primary className="grid divide-y divide-kumo-line sm:grid-cols-3 sm:divide-x sm:divide-y-0">
          {[
            { label: "Invocations", value: compactNumber(traffic.invocations) },
            { label: "Errors", value: compactNumber(traffic.errors) },
            { label: "CPU time", value: `${formatMilliseconds(traffic.duration_ms_avg)}` },
          ].map(({ label, value }) => (
            <div className="px-5 py-4" key={label}>
              <Text size="sm" variant="secondary">
                {label}
              </Text>
              <div className="mt-2">
                <Text as="p" variant="heading3">
                  {value}
                </Text>
              </div>
            </div>
          ))}
        </LayerCard.Primary>
      </LayerCard>

      <LayerCard>
        <LayerCard.Secondary className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <Text as="h2" variant="heading3">
              Versions
            </Text>
          </div>
          <button
            type="button"
            onClick={onOpenDeployments}
            className="text-sm text-kumo-link hover:underline"
          >
            Open deployments tab
          </button>
        </LayerCard.Secondary>
        <LayerCard.Primary className="overflow-x-auto">
          <WorkerDeploymentsTable deployments={recentDeployments} />
        </LayerCard.Primary>
      </LayerCard>
    </div>
  );
}

function WorkerFileViewer({
  files,
  selectedFile,
  onSelect,
}: {
  files: WorkerFile[];
  selectedFile?: WorkerFile;
  onSelect: (file: WorkerFile) => void;
}) {
  if (!selectedFile)
    return (
      <WorkerDetailEmpty
        icon={<FileCode2 />}
        title="No deployed bundle"
        copy="Deploy this worker to inspect its bundle file."
      />
    );
  return (
    <div className="grid min-h-[510px] overflow-hidden rounded-xl border border-gray-200 md:grid-cols-[190px_1fr]">
      <aside className="border-b border-gray-200 bg-gray-50 py-3 md:border-b-0 md:border-r">
        <div className="flex items-center gap-2 px-4 py-1.5 font-mono text-[10px] font-bold text-gray-700">
          <Folder className="size-3.5 text-blue-600" />
          active
        </div>
        {files.map((file) => (
          <button
            key={file.path}
            onClick={() => onSelect(file)}
            className={cn(
              "flex w-full items-center gap-2 px-4 py-2 pl-8 text-left font-mono text-[10px] transition",
              selectedFile.path === file.path
                ? "bg-gray-200 font-bold text-gray-900"
                : "text-gray-600 hover:bg-white hover:text-gray-900",
            )}
          >
            {file.name.endsWith(".json") ? (
              <FileJson className="size-3.5 text-orange-600" />
            ) : (
              <FileCode2 className="size-3.5 text-green-700" />
            )}
            {file.name}
          </button>
        ))}
      </aside>
      <div className="min-w-0 bg-[#202b29] text-[#d8dfd8]">
        <div className="flex items-center justify-between border-b border-white/10 px-4 py-3">
          <p className="font-mono text-[10px] text-[#b5c1bb]">{selectedFile.path}</p>
          <span className="font-mono text-[9px]   text-[#778781]">
            {formatBytes(selectedFile.size)} / read only
          </span>
        </div>
        <pre className="overflow-x-auto p-4 font-mono text-[11px] leading-6">
          <code>
            {selectedFile.content.split("\n").map((line, index) => (
              <span key={`${line}-${index}`} className="block">
                <span className="mr-5 inline-block w-5 select-none text-right text-[#61706b]">
                  {index + 1}
                </span>
                {line || " "}
              </span>
            ))}
          </code>
        </pre>
      </div>
    </div>
  );
}

function WorkerConfig({
  detail,
  worker,
  apiConnected,
  notify,
}: {
  detail?: WorkerDetailData;
  worker: { id: string; name: string; hostname: string; created_at: string; created_by?: string };
  apiConnected: boolean;
  notify: (text: string) => void;
}) {
  const [protectedRoutes, setProtectedRoutes] = useState(
    (detail?.app.auth?.protected_routes ?? []).join("\n"),
  );
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setProtectedRoutes((detail?.app.auth?.protected_routes ?? []).join("\n"));
  }, [detail?.app.id, detail?.app.auth?.protected_routes]);

  const app = detail?.app ?? worker;
  const deployment = detail?.deployment;
  const workerID = app.id;
  const rows = [
    ["Worker ID", app.id],
    ["Name", app.name],
    ["Hostname", app.hostname],
    ["Worker created by", app.created_by ?? "-"],
    ["Created", new Date(app.created_at).toLocaleString()],
    ["Deployment", deployment?.id ?? "awaiting deploy"],
    ["Deployment created by", deployment?.created_by ?? "-"],
    ["Commit", deployment?.commit_hash ? shortCommitHash(deployment.commit_hash) : "-"],
    ["Commit message", deployment?.commit_message ?? "-"],
    ["Compatibility date", deployment?.compatibility_date ?? "-"],
    ["Compatibility flags", deployment?.compatibility_flags?.join(", ") || "-"],
    ["Entrypoint", deployment?.entrypoint ?? "-"],
    ["Deployed", deployment ? new Date(deployment.created_at).toLocaleString() : "-"],
  ];

  async function saveRoutes() {
    if (!apiConnected) {
      notify("Protected routes are only editable when nanoflared is connected");
      return;
    }
    setSaving(true);
    try {
      const protected_routes = protectedRoutes
        .split("\n")
        .map((route) => route.trim())
        .filter(Boolean);
      const response = await apiFetch(`/v1/workers/${workerID}`, {
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

  const vars = deployment?.vars ? Object.entries(deployment.vars) : [];
  const secrets = detail?.secrets ?? [];
  const crons = deployment?.triggers?.crons ?? [];

  return (
    <div className="">
      <div className="overflow-hidden rounded-lg border border-[#e2ddd2]">
        {rows.map(([label, value]) => (
          <div
            key={label}
            className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]"
          >
            <span className="font-mono text-[10px]   text-[#93978f]">{label}</span>
            <span className="break-all font-mono text-[11px] font-bold text-[#4f5a55]">
              {value}
            </span>
          </div>
        ))}
      </div>
      {!deployment && (
        <WorkerDetailEmpty
          icon={<SlidersHorizontal />}
          title="No runtime config"
          copy="Deploy this worker to generate its active workerd configuration."
        />
      )}
      <div className="mt-5 overflow-hidden rounded-lg border border-[#e2ddd2] bg-white/50">
        <div className="border-b border-[#e8e3d9] px-4 py-3">
          <p className="font-mono text-[10px]   text-[#7f857e]">Cron triggers</p>
        </div>
        {crons.length ? (
          <table className="w-full text-left">
            <thead>
              <tr className="border-b border-[#e8e3d9] font-mono text-[9px] text-[#989b95]">
                <th className="px-4 py-3">Schedule</th>
                <th className="pr-4">Timezone</th>
              </tr>
            </thead>
            <tbody>
              {crons.map((cron) => (
                <tr key={cron} className="border-b border-[#ece7dc] text-xs last:border-0">
                  <td className="px-4 py-3 font-mono text-[10px] font-bold text-[#4f5a55]">
                    {cron}
                  </td>
                  <td className="pr-4 py-3 font-mono text-[10px] text-[#7f857e]">UTC</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="px-4 py-4 text-xs text-[#6f766f]">No cron triggers configured.</p>
        )}
      </div>
      <div className="mt-5 overflow-hidden rounded-lg border border-[#e2ddd2] bg-white/50">
        <div className="border-b border-[#e8e3d9] px-4 py-3">
          <p className="font-mono text-[10px]   text-[#7f857e]">Environment vars</p>
        </div>
        {vars.length ? (
          <table className="w-full text-left">
            <thead>
              <tr className="border-b border-[#e8e3d9] font-mono text-[9px] text-[#989b95]">
                <th className="px-4 py-3">Name</th>
                <th className="pr-4">Value</th>
              </tr>
            </thead>
            <tbody>
              {vars.map(([name, value]) => (
                <tr
                  key={name}
                  className="border-b border-[#ece7dc] align-top text-xs last:border-0"
                >
                  <td className="px-4 py-3 font-mono text-[10px] font-bold text-[#4f5a55]">
                    {name}
                  </td>
                  <td className="pr-4 py-3">
                    <pre className="overflow-x-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-[#5f6863]">
                      <code>{JSON.stringify(value)}</code>
                    </pre>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="px-4 py-4 text-xs text-[#6f766f]">No deployment vars configured.</p>
        )}
      </div>
      <div className="mt-5 overflow-hidden rounded-lg border border-[#e2ddd2] bg-white/50">
        <div className="border-b border-[#e8e3d9] px-4 py-3">
          <p className="font-mono text-[10px]   text-[#7f857e]">Secrets</p>
        </div>
        {secrets.length ? (
          <table className="w-full text-left">
            <thead>
              <tr className="border-b border-[#e8e3d9] font-mono text-[9px] text-[#989b95]">
                <th className="px-4 py-3">Name</th>
                <th className="pr-4">Updated</th>
              </tr>
            </thead>
            <tbody>
              {secrets.map((secret) => (
                <tr key={secret.name} className="border-b border-[#ece7dc] text-xs last:border-0">
                  <td className="px-4 py-3 font-mono text-[10px] font-bold text-[#4f5a55]">
                    {secret.name}
                  </td>
                  <td className="pr-4 py-3 font-mono text-[10px] text-[#7f857e]">
                    {new Date(secret.updated_at).toLocaleString()}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="px-4 py-4 text-xs text-[#6f766f]">No secrets configured.</p>
        )}
      </div>
      <div className="mt-5 rounded-lg border border-[#e2ddd2] bg-white/50 p-4">
        <div className="mb-2 flex items-center justify-between">
          <p className="font-mono text-[10px]   text-[#7f857e]">Protected routes</p>
          <Button type="button" onClick={() => void saveRoutes()} disabled={saving}>
            <Save className="size-3.5" />
            Save routes
          </Button>
        </div>
        <p className="mb-3 text-xs text-[#6f766f]">
          One absolute path per line. Example: <span className="font-mono">/admin/*</span>
        </p>
        <textarea
          value={protectedRoutes}
          onChange={(event) => setProtectedRoutes(event.target.value)}
          spellCheck={false}
          className="min-h-40 w-full rounded-md border border-[#d6d0c3] bg-[#fdfbf6] p-3 font-mono text-[11px] leading-6 text-[#35413e] outline-none"
          placeholder="/admin/*&#10;/api/private/*"
        />
      </div>
    </div>
  );
}

function WorkerDeployments({
  deployments,
  notify,
  onSaved,
  traffic,
  workerID,
}: {
  deployments: ConsoleDeployment[];
  notify: (text: string) => void;
  onSaved: (deployments: ConsoleDeployment[]) => void;
  traffic: WorkerTraffic;
  workerID: string;
}) {
  const sorted = [...deployments].sort(
    (left, right) => new Date(right.created_at).getTime() - new Date(left.created_at).getTime(),
  );
  const [draft, setDraft] = useState<Record<string, number>>({});
  const [saving, setSaving] = useState(false);
  const [splitOpen, setSplitOpen] = useState(false);
  const [splitTargetID, setSplitTargetID] = useState<string>();

  useEffect(() => {
    setDraft(
      Object.fromEntries(
        sorted.map((deployment) => [deployment.id, deployment.traffic_percent || 0]),
      ),
    );
  }, [deployments]);

  if (!deployments.length)
    return (
      <WorkerDetailEmpty
        icon={<Archive />}
        title="No deployment history"
        copy="This worker has no recorded revisions yet."
      />
    );

  const active = sorted.filter(
    (deployment) => (draft[deployment.id] ?? deployment.traffic_percent ?? 0) > 0,
  );
  const current = active[0] ?? sorted[0];
  const activeRows = active.length ? active : current ? [current] : [];
  const firstInactive = sorted.find(
    (deployment) =>
      deployment.id !== current?.id &&
      (draft[deployment.id] ?? deployment.traffic_percent ?? 0) <= 0,
  );
  const splitTarget =
    sorted.find((deployment) => deployment.id === splitTargetID && deployment.id !== current?.id) ??
    firstInactive;
  const splitRows = [splitTarget, current].filter(
    (deployment): deployment is ConsoleDeployment => !!deployment,
  );
  const total = Object.values(draft).reduce(
    (sum, value) => sum + (Number.isFinite(value) ? value : 0),
    0,
  );
  const valid = total === 100 && Object.values(draft).some((value) => value > 0);

  function openSplit(deployment?: ConsoleDeployment) {
    const target = deployment && deployment.id !== current?.id ? deployment : firstInactive;
    if (!target) {
      notify("No inactive deployment available to split traffic.");
      return;
    }
    setSplitTargetID(target.id);
    setSplitOpen(true);
  }

  async function saveDeploymentTraffic(
    next: { id: string; traffic_percent: number }[],
    successMessage: string,
  ) {
    const totalPercent = next.reduce((sum, deployment) => sum + deployment.traffic_percent, 0);
    if (totalPercent !== 100 || next.length === 0) {
      notify("Traffic percentages must total 100%.");
      return;
    }
    setSaving(true);
    try {
      const response = await apiFetch(`/v1/workers/${workerID}/deployments/traffic`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ deployments: next }),
      });
      if (!response.ok) throw new Error(await errorText(response, "Could not save traffic split"));
      const updated = (await response.json()) as ConsoleDeployment[];
      onSaved(updated);
      notify(successMessage);
      setSplitOpen(false);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Could not save traffic split");
    } finally {
      setSaving(false);
    }
  }

  async function saveTraffic() {
    const next = sorted
      .map((deployment) => ({
        id: deployment.id,
        traffic_percent: Math.trunc(draft[deployment.id] || 0),
      }))
      .filter((deployment) => deployment.traffic_percent > 0);
    if (!valid) {
      notify("Traffic percentages must total 100%.");
      return;
    }
    await saveDeploymentTraffic(next, "Traffic split updated.");
  }

  async function rollbackDeployment(deployment: ConsoleDeployment) {
    await saveDeploymentTraffic(
      [{ id: deployment.id, traffic_percent: 100 }],
      "Deployment rolled back.",
    );
  }

  return (
    <div className="min-h-[510px] w-full space-y-7">
      <LayerCard>
        <LayerCard.Secondary>
          <Text as="h2" variant="heading3">
            Active deployment
          </Text>
        </LayerCard.Secondary>
        <LayerCard.Primary className="overflow-x-auto p-1">
          <Table className="min-w-[920px]">
            <Table.Header>
              <Table.Row>
                <Table.Head>Version ID</Table.Head>
                <Table.Head>Deployed</Table.Head>
                <Table.Head>Traffic %</Table.Head>
                <Table.Head>Requests/sec</Table.Head>
                <Table.Head>Error Rate</Table.Head>
                <Table.Head>Median CPU Time</Table.Head>
                <Table.Head aria-label="Traffic history" />
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {activeRows.map((deployment) => (
                <Table.Row key={deployment.id}>
                  <Table.Cell>
                    <DeploymentID id={deployment.id} />
                  </Table.Cell>
                  <Table.Cell>{formatAge(deployment.created_at)}</Table.Cell>
                  <Table.Cell>
                    <div className="flex min-w-48 items-center gap-3">
                      <span>{draft[deployment.id] ?? deployment.traffic_percent}%</span>
                      <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-kumo-tint">
                        <div
                          className="h-full rounded-full bg-kumo-info"
                          style={{
                            width: `${draft[deployment.id] ?? deployment.traffic_percent}%`,
                          }}
                        />
                      </div>
                    </div>
                  </Table.Cell>
                  <Table.Cell>
                    {traffic.available
                      ? `${traffic.requests_per_second.toFixed(2)} req/sec`
                      : "0 req/sec"}
                  </Table.Cell>
                  <Table.Cell>
                    {traffic.available ? `${traffic.error_rate.toFixed(2)}%` : "0%"}
                  </Table.Cell>
                  <Table.Cell>
                    {traffic.duration_ms_avg ? formatMilliseconds(traffic.duration_ms_avg) : "0 ms"}
                  </Table.Cell>
                  <Table.Cell>
                    <MiniInlineChart values={traffic.traffic} />
                  </Table.Cell>
                </Table.Row>
              ))}
            </Table.Body>
          </Table>
        </LayerCard.Primary>
      </LayerCard>

      <section>
        <div className="mb-4 flex items-end justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-base font-semibold text-[#2f2f2f]">Version History</h3>
            </div>
          </div>
          <Tooltip
            content="Refresh deployments"
            render={
              <Button
                shape="square"
                variant="secondary"
                onClick={() => notify("Deployments refresh automatically.")}
                aria-label="Refresh deployments"
              >
                <RefreshCw className="size-4" />
              </Button>
            }
          />
        </div>
        <div className="overflow-x-auto rounded-lg border border-[#dedede] bg-white">
          <WorkerDeploymentsTable
            activeCount={active.length}
            deployments={sorted}
            draft={draft}
            onRollback={(deployment) => void rollbackDeployment(deployment)}
            onSplit={openSplit}
          />
        </div>
        <div className="mt-6 flex items-center justify-between text-sm text-[#5f5f5f]">
          <span>
            Showing 1-{sorted.length} of {sorted.length}
          </span>
          <div className="flex overflow-hidden rounded-lg border border-[#e9e9e9] bg-white text-[#222]">
            <button className="h-9 w-11 text-[#d2d2d2]" disabled type="button">
              ‹‹
            </button>
            <button
              className="h-9 w-11 border-l border-[#ececec] text-[#d2d2d2]"
              disabled
              type="button"
            >
              ‹
            </button>
            <span className="grid h-9 w-12 place-items-center border-l border-[#ececec]">1</span>
            <button
              className="h-9 w-11 border-l border-[#ececec] text-[#d2d2d2]"
              disabled
              type="button"
            >
              ›
            </button>
            <button
              className="h-9 w-11 border-l border-[#ececec] text-[#d2d2d2]"
              disabled
              type="button"
            >
              ››
            </button>
          </div>
        </div>
      </section>

      <Dialog.Root open={splitOpen} onOpenChange={(open) => !open && setSplitOpen(false)}>
        <Dialog className="p-6" size="xl">
          <div className="flex items-start justify-between gap-4">
            <Dialog.Title className="text-lg font-semibold">Split deployment across versions</Dialog.Title>
            <Dialog.Close
              render={(props) => (
                <Button {...props} aria-label="Close" shape="square" size="sm" variant="ghost">
                  <X className="size-4" />
                </Button>
              )}
            />
          </div>
          <div className="space-y-5 pt-4">
            {current && (
              <SplitSection title="Current deployed version">
                <SplitDeploymentRow
                  deployment={current}
                  percent={draft[current.id] ?? current.traffic_percent}
                  readOnly
                />
              </SplitSection>
            )}
            <div className="grid place-items-center text-[#98a2b3]">
              <ArrowDown className="size-5" />
            </div>
            <SplitSection title="New deployment">
              {splitRows.map((deployment) => (
                <SplitDeploymentRow
                  key={deployment.id}
                  deployment={deployment}
                  percent={draft[deployment.id] ?? deployment.traffic_percent ?? 0}
                  readOnly={deployment.id !== splitTarget?.id}
                  onChange={(value) =>
                    setDraft((currentDraft) => {
                      const clamped = Math.max(0, Math.min(100, value));
                      if (deployment.id === splitTarget?.id && current?.id) {
                        return {
                          ...currentDraft,
                          [deployment.id]: clamped,
                          [current.id]: 100 - clamped,
                        };
                      }
                      return { ...currentDraft, [deployment.id]: clamped };
                    })
                  }
                />
              ))}
            </SplitSection>
            <div className="flex items-center justify-end gap-2 pt-1">
              <Button type="button" variant="outline" onClick={() => setSplitOpen(false)}>
                Cancel
              </Button>
              <Button type="button" onClick={() => void saveTraffic()} disabled={saving || !valid}>
                Deploy
              </Button>
            </div>
          </div>
        </Dialog>
      </Dialog.Root>
    </div>
  );
}

function WorkerOutput({
  workerID,
  deployments,
  initialLines,
}: {
  workerID: string;
  deployments: ConsoleDeployment[];
  initialLines: WorkerOutputLine[];
}) {
  const [lines, setLines] = useState(initialLines);
  const [deploymentID, setDeploymentID] = useState("");
  const [level, setLevel] = useState("");
  const [search, setSearch] = useState("");
  const [follow, setFollow] = useState(true);
  const [loading, setLoading] = useState(false);

  useEffect(() => setLines(initialLines), [initialLines]);
  useEffect(() => {
    let cancelled = false;
    async function load() {
      setLoading(true);
      const params = new URLSearchParams({ limit: "500" });
      if (deploymentID) params.set("deployment_id", deploymentID);
      if (level) params.set("level", level);
      if (search.trim()) params.set("q", search.trim());
      const next = await fetchJSON<WorkerOutputLine[]>(`/v1/workers/${workerID}/output?${params}`).catch(() => []);
      if (!cancelled) {
        setLines(next);
        setLoading(false);
      }
    }
    void load();
    if (!follow) return () => { cancelled = true; };
    const timer = window.setInterval(() => void load(), 5000);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [workerID, deploymentID, level, search, follow]);

  return (
    <div className="min-h-[510px] bg-[#202b29] p-4 rounded-lg">
      <div className="mb-3 flex items-center gap-2 font-mono text-[9px] text-[#82928c]">
        <span className="size-1.5 rounded-full bg-[#78b88b]" />
        Deployment output {loading && <span className="text-[#9fb0aa]">syncing</span>}
      </div>
      <div className="mb-4 grid gap-2 sm:grid-cols-[minmax(0,1fr)_130px_130px_auto]">
        <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Filter output" className="!bg-[#18211f] !border-[#40504b] !text-[#d7e1dc]" />
        <select value={deploymentID} onChange={(event) => setDeploymentID(event.target.value)} className="rounded border border-[#40504b] bg-[#18211f] px-2 font-mono text-[11px] text-[#d7e1dc]">
          <option value="">All deployments</option>
          {deployments.map((deployment) => <option key={deployment.id} value={deployment.id}>{shortDeploymentID(deployment.id)}</option>)}
        </select>
        <select value={level} onChange={(event) => setLevel(event.target.value)} className="rounded border border-[#40504b] bg-[#18211f] px-2 font-mono text-[11px] text-[#d7e1dc]">
          <option value="">All levels</option><option value="error">Error</option><option value="warn">Warn</option><option value="info">Info</option>
        </select>
        <Button variant="outline" size="sm" onClick={() => setFollow((value) => !value)} className="border-[#40504b] !text-[#c6d0cb]">{follow ? "Following" : "Paused"}</Button>
      </div>
      {lines.length ? (
        <div className="space-y-1.5">
          {lines.map(({ timestamp, level, message, deployment_id }, index) => (
            <p
              key={`${timestamp}-${index}`}
              className="whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-[#c6d0cb]"
            >
              <span className="mr-3 text-[#71817b]">
                {new Date(timestamp).toLocaleTimeString()}
              </span>
              <span
                className={cn(
                  "mr-3",
                  level === "error"
                    ? "text-[#e87962]"
                    : level === "warn"
                      ? "text-[#e3a65a]"
                      : "text-[#78b88b]",
                )}
              >
                {level.toUpperCase()}
              </span>
              {deployment_id && (
                <span className="mr-3 rounded border border-[#40504b] px-1.5 py-0.5 text-[9px] text-[#9fb0aa]">
                  {shortDeploymentID(deployment_id)}
                </span>
              )}
              {message}
            </p>
          ))}
        </div>
      ) : (
        <p className="pt-16 text-center font-mono text-[10px]   text-[#71817b]">
          No deployment output captured yet
        </p>
      )}
    </div>
  );
}

function MiniTrafficChart({ values }: { values: number[] }) {
  if (!values.length) return <EmptyMetrics />;
  const now = Date.now();
  const data = values.map(
    (value, index) => [now - (values.length - 1 - index) * 60_000, value] as [number, number],
  );
  const lastTimestamp = data[data.length - 1][0];
  return (
    <TimeseriesChart
      ariaDescription="Worker requests per minute."
      data={[{ color: ChartPalette.categorical(0), data, name: "Traffic" }]}
      echarts={echarts}
      gradient
      height={208}
      tooltipValueFormat={(value) => `${value.toFixed(1)} requests`}
      xAxisTickFormat={(timestamp) =>
        timestamp === lastTimestamp ? "Now" : `${Math.round((lastTimestamp - timestamp) / 60_000)}m`
      }
      yAxisTickFormat={compactNumber}
    />
  );
}

function compactNumber(value: number) {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(
    value || 0,
  );
}

function formatMilliseconds(value: number) {
  return `${value.toFixed(2)} ms`;
}

function shortDeploymentID(id: string) {
  return id.length > 10 ? `${id.slice(0, 10)}` : id;
}

function shortCommitHash(hash: string) {
  return hash.length > 12 ? hash.slice(0, 12) : hash;
}

function hostnameHref(hostname: string) {
  return /^https?:\/\//i.test(hostname) ? hostname : `https://${hostname}`;
}

function WorkerDeploymentsTable({
  activeCount,
  deployments,
  draft,
  onRollback,
  onSplit,
}: {
  activeCount?: number;
  deployments: ConsoleDeployment[];
  draft?: Record<string, number>;
  onRollback?: (deployment: ConsoleDeployment) => void;
  onSplit?: (deployment: ConsoleDeployment) => void;
}) {
  if (!deployments.length)
    return (
      <div className="px-5 py-12">
        <WorkerDetailEmpty
          icon={<Archive />}
          title="No deployment history"
          copy="This worker has no recorded revisions yet."
        />
      </div>
    );
  return (
    <table className="w-full min-w-[860px] text-left">
      <tbody>
        {deployments.map((deployment) => {
          const percent = draft?.[deployment.id] ?? deployment.traffic_percent ?? 0;
          const isInactive = percent <= 0;
          const showMenu = !!onSplit && (isInactive || (activeCount ?? 0) > 1);
          return (
            <tr
              key={deployment.id}
              className="group border-b border-[#e6e6e6] text-sm transition last:border-0 hover:bg-[#fafafa]"
            >
              <td className="relative w-28 px-5 py-4">
                {percent > 0 && (
                  <span className="absolute left-2 top-4 h-7 w-1 rounded-full bg-[#1677ff]" />
                )}
                <DeploymentID id={deployment.id} />
                {/* {deployment.commit_hash && <div className="mt-1 pl-5 font-mono text-[10px] text-[#8a8a8a]">{shortCommitHash(deployment.commit_hash)}</div>} */}
              </td>
              <td className="w-8 py-4">
                <Tooltip
                  content="Copy version ID"
                  render={
                    <Button
                      className="text-[#777] opacity-80 hover:text-[#1f1f1f] group-hover:opacity-100"
                      shape="square"
                      variant="ghost"
                      onClick={() => void navigator.clipboard?.writeText(deployment.id)}
                      aria-label="Copy version ID"
                    >
                      <Copy className="size-3.5" />
                    </Button>
                  }
                />
              </td>
              <td
                className="max-w-[360px] truncate px-2 py-4 italic text-[#727272]"
                title={deployment.commit_message || undefined}
              >
                {deployment.commit_message || "Manually deployed"}
              </td>
              <td className="px-4 py-4 text-right text-[#242424]">
                <span className="text-[#777]">{deployment.created_by || "local"}</span>
              </td>
              <td className="w-24 px-2 py-4 text-[#333]">{formatAge(deployment.created_at)}</td>
              <td className="w-12 px-4 py-4 text-right">
                {showMenu && (
                  <DropdownMenu>
                    <DropdownMenu.Trigger>
                      <button
                        className="inline-grid size-7 place-items-center rounded-md text-[#2e2e2e] hover:bg-[#f1f1f1]"
                        type="button"
                        aria-label="Deployment actions"
                      >
                        <MoreHorizontal className="size-4" />
                      </button>
                    </DropdownMenu.Trigger>
                    <DropdownMenu.Content align="end">
                      <DropdownMenu.Item onClick={() => onSplit?.(deployment)}>
                        Split versions
                      </DropdownMenu.Item>
                      {isInactive && (
                        <DropdownMenu.Item onClick={() => onRollback?.(deployment)}>
                          Rollback
                        </DropdownMenu.Item>
                      )}
                    </DropdownMenu.Content>
                  </DropdownMenu>
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function DeploymentID({ id }: { id: string }) {
  return (
    <span className="inline-flex items-center gap-2 font-mono text-[12px] text-[#727272]">
      {shortDeploymentID(id)}
    </span>
  );
}

function SplitSection({ children, title }: { children: ReactNode; title: string }) {
  return (
    <section className="overflow-hidden rounded-lg border border-[#dedede]">
      <div className="bg-[#fafafa] px-4 py-2 text-sm text-[#666]">{title}</div>
      <div className="divide-y divide-[#eeeeee]">{children}</div>
    </section>
  );
}

function SplitDeploymentRow({
  deployment,
  onChange,
  percent,
  readOnly,
}: {
  deployment: ConsoleDeployment;
  onChange?: (value: number) => void;
  percent: number;
  readOnly?: boolean;
}) {
  return (
    <div className="grid min-h-14 grid-cols-[minmax(100px,1fr)_minmax(160px,1.5fr)_minmax(150px,1fr)_96px] items-center gap-3 px-4 py-3 text-sm">
      <div>
        <div className="flex items-center gap-2">
          <DeploymentID id={deployment.id} />
          <Copy className="size-3.5 text-[#888]" />
        </div>
        {deployment.commit_hash && (
          <div className="mt-1 pl-5 font-mono text-[10px] text-[#8a8a8a]">
            {shortCommitHash(deployment.commit_hash)}
          </div>
        )}
      </div>
      {/* <div className="truncate italic text-[#727272]" title={deployment.commit_message || undefined}>{deployment.commit_message || "Manually deployed"}</div>
      <div className="text-right text-[#242424]">Nanoflare <span className="text-[#777]">by local</span> <span className="ml-2 text-[#777]">{formatAge(deployment.created_at)}</span></div> */}
      <div className="flex items-center justify-end gap-1">
        {readOnly ? (
          <span className="font-semibold text-[#242424]">{percent}%</span>
        ) : (
          <Input
            className="w-14 text-center"
            max={100}
            min={0}
            type="number"
            value={percent}
            onChange={(event) => onChange?.(Number(event.target.value) || 0)}
          />
        )}
        {!readOnly && <span className="text-[#555]">%</span>}
      </div>
    </div>
  );
}

function MiniInlineChart({ values }: { values: number[] }) {
  if (!values.length)
    return (
      <div className="relative h-7 w-56 overflow-hidden rounded">
        <div className="absolute inset-x-0 top-3 border-t border-[#dedede]" />
        <span className="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 rounded-full border border-[#dedede] bg-[#fafafa] px-3 py-1 text-xs text-[#777]">
          No data
        </span>
      </div>
    );
  const max = Math.max(...values, 1);
  const points = values
    .map(
      (value, index) =>
        `${(index / Math.max(values.length - 1, 1)) * 220},${26 - (value / max) * 20}`,
    )
    .join(" ");
  return (
    <svg className="h-7 w-56" viewBox="0 0 220 28" aria-hidden="true">
      <polyline fill="none" points={points} stroke="#b8c3d8" strokeWidth="1.5" />
    </svg>
  );
}

function formatAge(value: string) {
  const diffMs = Math.max(0, Date.now() - new Date(value).getTime());
  const minutes = Math.floor(diffMs / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
