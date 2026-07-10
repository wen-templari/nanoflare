import { lazy, Suspense, useEffect, useState } from "react"
import {
  Activity, AlertTriangle, Archive, ArrowLeft, Copy, FileCode2, FileJson, Folder,
  GitBranch, Globe2, Save, SlidersHorizontal, Terminal,
} from "lucide-react"
import { Navigate, useNavigate, useParams } from "react-router-dom"
import { fetchJSON } from "../app/api"
import { useWorkspace } from "../app/workspace-context"
import type {
  ConsoleDeployment, WorkerDeployment, WorkerDetailData, WorkerDetailTab, WorkerFile,
  WorkerOutputLine, WorkerTraffic,
} from "../app/types"
import { formatBytes } from "../app/utils"
import { EmptyMetrics, Panel, StatusCodeMix, WorkerDetailEmpty } from "../components/shared/primitives"
import { Badge } from "../components/ui/badge"
import { Button } from "../components/ui/button"
import { cn } from "../lib/utils"

const WorkerDefinitionFlow = lazy(() =>
  import("../components/worker-definition-flow").then((module) => ({ default: module.WorkerDefinitionFlow })),
)

export function WorkerDetailPage() {
  const navigate = useNavigate()
  const { workerId } = useParams()
  const { workers, notify, apiConnected } = useWorkspace()
  const worker = workers.find((item) => item.id === workerId)

  if (!worker) return <Navigate to="/workers" replace />

  return <WorkerDetailContent worker={worker} onBack={() => navigate("/workers")} notify={notify} apiConnected={apiConnected} />
}

function WorkerDetailContent({ worker, onBack, notify, apiConnected }: { worker: { id: string; name: string; hostname: string; bindings?: WorkerDeployment["bindings"]; created_at: string }; onBack: () => void; notify: (text: string) => void; apiConnected: boolean }) {
  const navigate = useNavigate()
  const { namespaces } = useWorkspace()
  const [tab, setTab] = useState<WorkerDetailTab>("overview")
  const [detail, setDetail] = useState<WorkerDetailData>()
  const [files, setFiles] = useState<WorkerFile[]>([])
  const [deployments, setDeployments] = useState<ConsoleDeployment[]>([])
  const [selectedFile, setSelectedFile] = useState<WorkerFile>()
  const [output, setOutput] = useState<WorkerOutputLine[]>([])
  const [traffic, setTraffic] = useState<WorkerTraffic>({ available: false, requests_per_second: 0, p95_latency: 0, error_rate: 0, invocations: 0, errors: 0, bundle_size: 0, traffic: [], status_codes: [] })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")

  useEffect(() => {
    let cancelled = false

    async function refresh() {
      if (!apiConnected) {
        setDetail(undefined)
        setFiles([])
        setDeployments([])
        setOutput([])
        setTraffic({ available: false, requests_per_second: 0, p95_latency: 0, error_rate: 0, invocations: 0, errors: 0, bundle_size: 0, traffic: [], status_codes: [] })
        setError("Worker detail API unavailable")
        setLoading(false)
        return
      }

      const [nextDetail, nextFiles, nextDeployments, nextOutput, nextTraffic] = await Promise.all([
        fetchJSON<WorkerDetailData>(`/v1/apps/${worker.id}`).catch(() => undefined),
        fetchJSON<WorkerFile[]>(`/v1/apps/${worker.id}/files`).catch(() => []),
        fetchJSON<ConsoleDeployment[]>(`/v1/apps/${worker.id}/deployments`).catch(() => []),
        fetchJSON<WorkerOutputLine[]>(`/v1/apps/${worker.id}/output`).catch(() => []),
        fetchJSON<WorkerTraffic>(`/v1/apps/${worker.id}/traffic`).catch(() => ({ available: false, requests_per_second: 0, p95_latency: 0, error_rate: 0, invocations: 0, errors: 0, bundle_size: 0, traffic: [], status_codes: [] })),
      ])
      if (cancelled) return
      setDetail(nextDetail)
      setFiles(nextFiles)
      setDeployments(nextDeployments)
      setOutput(nextOutput)
      setTraffic(nextTraffic)
      setError(nextDetail ? "" : "Worker detail API unavailable")
      setSelectedFile((current) => nextFiles.find((file) => file.path === current?.path) ?? nextFiles[0])
      setLoading(false)
    }

    void refresh()
    const interval = window.setInterval(() => void refresh(), 15000)
    return () => {
      cancelled = true
      window.clearInterval(interval)
    }
  }, [apiConnected, worker])

  const deployment = detail?.deployment
  const recentDeployments = [...deployments]
    .sort((left, right) => new Date(right.created_at).getTime() - new Date(left.created_at).getTime())
    .slice(0, 5)

  return (
    <>
      <button onClick={onBack} className="animate-rise mb-5 flex items-center gap-2 font-mono text-[10px] font-bold   text-[#77817a] transition hover:text-[#d75a41]"><ArrowLeft className="size-3.5" />All workers</button>
      <div className="animate-rise mb-6 flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <h1 className="text-4xl  text-[#26332f] md:text-5xl">{worker.name}</h1>
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 font-mono text-[10px] text-[#858b84]"><span className="flex items-center gap-1.5"><Globe2 className="size-3" />{worker.hostname}</span><span className="flex items-center gap-1.5"><GitBranch className="size-3" />{deployment?.id ?? "awaiting deploy"}</span></div>
        </div>
        <Button variant="outline" onClick={() => notify(`${worker.name} hostname copied`)}><Copy className="size-3.5" />Copy hostname</Button>
      </div>

      {(loading || error) && <div className={cn("mt-6 rounded-lg border px-4 py-3 font-mono text-[10px]  ", error ? "border-[#ecc3b6] bg-[#fae5df] text-[#b14b37]" : "border-[#dcd6ca] bg-white/45 text-[#8c918b]")}>{error || "Loading worker detail from nanoflared"}</div>}

      <section className="paper-panel animate-rise mt-6 overflow-hidden rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-[#e7e1d6] px-4 py-3">
          <div className="flex gap-1">
            {([
              { id: "overview", label: "Overview", icon: GitBranch },
              { id: "deployments", label: "Deployments", icon: Archive },
              { id: "files", label: "Files", icon: FileCode2 },
              { id: "output", label: "Output", icon: Terminal },
              { id: "settings", label: "Settings", icon: SlidersHorizontal },
            ] as const).map(({ id, label, icon: Icon }) => <button key={id} onClick={() => setTab(id)} className={cn("flex items-center gap-2 rounded-md px-3 py-2 font-mono text-[10px] font-bold   transition", tab === id ? "bg-[#26332f] text-white" : "text-[#80867f] hover:bg-[#efebe2] hover:text-[#35413e]")}><Icon className="size-3.5" />{label}</button>)}
          </div>
        </header>
        {tab === "overview" && <WorkerOverview worker={detail?.app ?? worker} deployment={detail?.deployment} namespaces={namespaces} onOpenNamespace={(namespaceID) => navigate(`/kv/${namespaceID}`)} onOpenBucket={(bucketID) => navigate(`/object-storage/${bucketID}`)} onOpenDeployments={() => setTab("deployments")} recentDeployments={recentDeployments} traffic={traffic} />}
        {tab === "deployments" && <WorkerDeployments deployments={deployments} />}
        {tab === "files" && <WorkerFileViewer files={files} selectedFile={selectedFile} onSelect={setSelectedFile} />}
        {tab === "output" && <WorkerOutput lines={output} />}
        {tab === "settings" && <WorkerConfig detail={detail} apiConnected={apiConnected} notify={notify} />}
      </section>
    </>
  )
}

function WorkerOverview({
  deployment,
  namespaces,
  onOpenNamespace,
  onOpenBucket,
  onOpenDeployments,
  recentDeployments,
  traffic,
  worker,
}: {
  deployment?: WorkerDeployment
  namespaces: { id: string; name: string; created_at: string }[]
  onOpenNamespace: (namespaceID: string) => void
  onOpenBucket: (bucketID: string) => void
  onOpenDeployments: () => void
  recentDeployments: ConsoleDeployment[]
  traffic: WorkerTraffic
  worker: WorkerDetailData["app"]
}) {
  const bindings = deployment?.bindings ?? worker.bindings ?? []
  const kvBindingCount = bindings.filter((binding) => binding.kind === "kv").length
  const objectBindingCount = bindings.filter((binding) => binding.kind === "object_storage_bucket").length
  const assetBinding = bindings.find((binding) => binding.kind === "asset")?.binding

  return (
    <div className="space-y-6 p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-extrabold text-[#26332f]">Overview</h2>
          <p className="mt-2 max-w-2xl text-sm leading-6 text-[#707670]">Trace how traffic enters this worker, inspect live request shape, and skim the latest rollout history without leaving the overview tab.</p>
        </div>
      </div>
      <div className="space-y-6">
        <section>
          <Suspense fallback={<div className="h-[420px] animate-pulse rounded-xl border border-[#e2ddd2] bg-[#f5f1e9]/70" />}>
            <WorkerDefinitionFlow worker={worker} deployment={deployment} namespaces={namespaces} onOpenNamespace={onOpenNamespace} onOpenBucket={onOpenBucket} />
          </Suspense>
        </section>

        <section className="grid gap-6 xl:grid-cols-2">
          <div className="grid gap-3 sm:grid-cols-3 xl:col-span-2">
            {[
              { label: "Invocations", value: compactNumber(traffic.invocations), note: "routed worker requests", icon: Activity },
              { label: "Errors", value: compactNumber(traffic.errors), note: "5xx responses", icon: AlertTriangle },
              { label: "Bundle", value: formatBytes(traffic.bundle_size || deployment?.bundle_size || 0), note: "active deployment size", icon: FileCode2 },
            ].map(({ label, value, note, icon: Icon }) => (
              <div key={label} className="rounded-lg border border-[#e2ddd2] bg-white/45 p-4">
                <div className="flex items-center justify-between">
                  <p className="font-mono text-[9px]   text-[#90958e]">{label}</p>
                  <Icon className="size-3.5 text-[#d75a41]" />
                </div>
                <p className="mt-3 text-2xl font-extrabold text-[#26332f]">{value}</p>
                <p className="mt-1 font-mono text-[9px]   text-[#999d97]">{note}</p>
              </div>
            ))}
          </div>
          <Panel title="Worker traffic" eyebrow={traffic.available ? "Last 60 minutes" : "Prometheus unavailable"}>
            <MiniTrafficChart values={traffic.traffic} />
          </Panel>
          <Panel title="Response codes" eyebrow="5 minute rate">
            <StatusCodeMix values={traffic.status_codes} />
          </Panel>
        </section>

        <section className="rounded-xl border border-[#e2ddd2] bg-white/45">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[#e7e1d6] px-5 py-4">
            <div>
              <p className="font-mono text-[9px]   text-[#d35c45]">Recent rollout</p>
              <h3 className="mt-1 text-sm font-extrabold text-[#26332f]">Recent deployments</h3>
            </div>
            <button
              type="button"
              onClick={onOpenDeployments}
              className="font-mono text-[10px] font-bold   text-[#d35c45] transition hover:text-[#b94a34]"
            >
              Open deployments tab
            </button>
          </div>
          <WorkerDeploymentsTable deployments={recentDeployments} />
        </section>
      </div>
    </div>
  )
}

function WorkerFileViewer({ files, selectedFile, onSelect }: { files: WorkerFile[]; selectedFile?: WorkerFile; onSelect: (file: WorkerFile) => void }) {
  if (!selectedFile) return <WorkerDetailEmpty icon={<FileCode2 />} title="No deployed bundle" copy="Deploy this worker to inspect its bundle file." />
  return (
    <div className="grid min-h-[510px] md:grid-cols-[190px_1fr]"><aside className="border-b border-[#e7e1d6] bg-[#f5f1e9]/75 py-3 md:border-b-0 md:border-r"><div className="flex items-center gap-2 px-4 py-1.5 font-mono text-[10px] font-bold text-[#68716c]"><Folder className="size-3.5 text-[#d75a41]" />active</div>{files.map((file) => <button key={file.path} onClick={() => onSelect(file)} className={cn("flex w-full items-center gap-2 px-4 py-2 pl-8 text-left font-mono text-[10px] transition", selectedFile.path === file.path ? "bg-[#e5e0d6] font-bold text-[#35413e]" : "text-[#848a83] hover:bg-white/60 hover:text-[#4c5853]")}>{file.name.endsWith(".json") ? <FileJson className="size-3.5 text-[#bd7e35]" /> : <FileCode2 className="size-3.5 text-[#668e7a]" />}{file.name}</button>)}</aside><div className="min-w-0 bg-[#202b29] text-[#d8dfd8]"><div className="flex items-center justify-between border-b border-white/10 px-4 py-3"><p className="font-mono text-[10px] text-[#b5c1bb]">{selectedFile.path}</p><span className="font-mono text-[9px]   text-[#778781]">{formatBytes(selectedFile.size)} / read only</span></div><pre className="overflow-x-auto p-4 font-mono text-[11px] leading-6"><code>{selectedFile.content.split("\n").map((line, index) => <span key={`${line}-${index}`} className="block"><span className="mr-5 inline-block w-5 select-none text-right text-[#61706b]">{index + 1}</span>{line || " "}</span>)}</code></pre></div></div>
  )
}

function WorkerConfig({ detail, apiConnected, notify }: { detail?: WorkerDetailData; apiConnected: boolean; notify: (text: string) => void }) {
  const [protectedRoutes, setProtectedRoutes] = useState((detail?.app.auth?.protected_routes ?? []).join("\n"))
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setProtectedRoutes((detail?.app.auth?.protected_routes ?? []).join("\n"))
  }, [detail?.app.id, detail?.app.auth?.protected_routes])

  if (!detail?.deployment) return <WorkerDetailEmpty icon={<SlidersHorizontal />} title="No runtime config" copy="Deploy this worker to generate its active workerd configuration." />
  const appID = detail.app.id
  const rows = [["Worker ID", detail.app.id], ["Name", detail.app.name], ["Hostname", detail.app.hostname], ["Deployment", detail.deployment.id], ["Compatibility date", detail.deployment.compatibility_date], ["Entrypoint", detail.deployment.entrypoint], ["Deployed", new Date(detail.deployment.created_at).toLocaleString()]]

  async function saveRoutes() {
    if (!apiConnected) {
      notify("Protected routes are only editable when nanoflared is connected")
      return
    }
    setSaving(true)
    try {
      const protected_routes = protectedRoutes.split("\n").map((route) => route.trim()).filter(Boolean)
      const response = await fetch(`/v1/apps/${appID}`, {
        method: "PATCH",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ auth: { protected_routes } }),
      })
      if (!response.ok) throw new Error(`Config update failed (${response.status})`)
      notify("Protected routes updated")
    } catch (error) {
      notify(error instanceof Error ? error.message : "Config update failed")
    } finally {
      setSaving(false)
    }
  }

  const vars = detail.deployment.vars ? Object.entries(detail.deployment.vars) : []
  const secrets = detail.secrets ?? []

  return <div className="p-5"><div className="overflow-hidden rounded-lg border border-[#e2ddd2]">{rows.map(([label, value]) => <div key={label} className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]"><span className="font-mono text-[10px]   text-[#93978f]">{label}</span><span className="font-mono text-[11px] font-bold text-[#4f5a55]">{value}</span></div>)}</div><div className="mt-5 overflow-hidden rounded-lg border border-[#e2ddd2] bg-white/50"><div className="border-b border-[#e8e3d9] px-4 py-3"><p className="font-mono text-[10px]   text-[#7f857e]">Environment vars</p></div>{vars.length ? <table className="w-full text-left"><thead><tr className="border-b border-[#e8e3d9] font-mono text-[9px] text-[#989b95]"><th className="px-4 py-3">Name</th><th className="pr-4">Value</th></tr></thead><tbody>{vars.map(([name, value]) => <tr key={name} className="border-b border-[#ece7dc] align-top text-xs last:border-0"><td className="px-4 py-3 font-mono text-[10px] font-bold text-[#4f5a55]">{name}</td><td className="pr-4 py-3"><pre className="overflow-x-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-[#5f6863]"><code>{JSON.stringify(value)}</code></pre></td></tr>)}</tbody></table> : <p className="px-4 py-4 text-xs text-[#6f766f]">No deployment vars configured.</p>}</div><div className="mt-5 overflow-hidden rounded-lg border border-[#e2ddd2] bg-white/50"><div className="border-b border-[#e8e3d9] px-4 py-3"><p className="font-mono text-[10px]   text-[#7f857e]">Secrets</p></div>{secrets.length ? <table className="w-full text-left"><thead><tr className="border-b border-[#e8e3d9] font-mono text-[9px] text-[#989b95]"><th className="px-4 py-3">Name</th><th className="pr-4">Updated</th></tr></thead><tbody>{secrets.map((secret) => <tr key={secret.name} className="border-b border-[#ece7dc] text-xs last:border-0"><td className="px-4 py-3 font-mono text-[10px] font-bold text-[#4f5a55]">{secret.name}</td><td className="pr-4 py-3 font-mono text-[10px] text-[#7f857e]">{new Date(secret.updated_at).toLocaleString()}</td></tr>)}</tbody></table> : <p className="px-4 py-4 text-xs text-[#6f766f]">No secrets configured.</p>}</div><div className="mt-5 rounded-lg border border-[#e2ddd2] bg-white/50 p-4"><div className="mb-2 flex items-center justify-between"><p className="font-mono text-[10px]   text-[#7f857e]">Protected routes</p><Button type="button" onClick={() => void saveRoutes()} disabled={saving}><Save className="size-3.5" />Save routes</Button></div><p className="mb-3 text-xs text-[#6f766f]">One absolute path per line. Example: <span className="font-mono">/admin/*</span></p><textarea value={protectedRoutes} onChange={(event) => setProtectedRoutes(event.target.value)} spellCheck={false} className="min-h-40 w-full rounded-md border border-[#d6d0c3] bg-[#fdfbf6] p-3 font-mono text-[11px] leading-6 text-[#35413e] outline-none" placeholder="/admin/*&#10;/api/private/*" /></div></div>
}

function WorkerDeployments({ deployments }: { deployments: ConsoleDeployment[] }) {
  if (!deployments.length) return <WorkerDetailEmpty icon={<Archive />} title="No deployment history" copy="This worker has no recorded revisions yet." />
  return <div className="min-h-[510px] overflow-x-auto"><WorkerDeploymentsTable deployments={[...deployments].sort((left, right) => new Date(right.created_at).getTime() - new Date(left.created_at).getTime())} /></div>
}

function WorkerOutput({ lines }: { lines: WorkerOutputLine[] }) {
  return <div className="min-h-[510px] bg-[#202b29] p-4"><div className="mb-4 flex items-center gap-2 font-mono text-[9px]   text-[#82928c]"><span className="size-1.5 rounded-full bg-[#78b88b]" />Shared workerd process output</div>{lines.length ? <div className="space-y-1.5">{lines.map(({ timestamp, level, message }, index) => <p key={`${timestamp}-${index}`} className="font-mono text-[11px] leading-5 text-[#c6d0cb]"><span className="mr-3 text-[#71817b]">{new Date(timestamp).toLocaleTimeString()}</span><span className={cn("mr-3", level === "error" ? "text-[#e87962]" : level === "warn" ? "text-[#e3a65a]" : "text-[#78b88b]")}>{level.toUpperCase()}</span>{message}</p>)}</div> : <p className="pt-16 text-center font-mono text-[10px]   text-[#71817b]">No runtime output captured yet</p>}</div>
}

function MiniTrafficChart({ values }: { values: number[] }) {
  const max = Math.max(...values, 0.01)
  if (!values.length) return <EmptyMetrics />
  return <><div className="flex h-32 items-end gap-1">{values.map((value, index) => <div key={index} title={`${value.toFixed(2)} requests/s`} className="flex-1 rounded-t-sm bg-[#bfd0c6] transition hover:bg-[#e25b3f]" style={{ height: `${Math.max((value / max) * 100, 2)}%` }} />)}</div><div className="mt-3 flex justify-between font-mono text-[9px] text-[#9ba09a]"><span>60 MIN AGO</span><span>NOW</span></div></>
}

function compactNumber(value: number) {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value || 0)
}

function WorkerDeploymentsTable({ deployments }: { deployments: ConsoleDeployment[] }) {
  if (!deployments.length) return <div className="px-5 py-12"><WorkerDetailEmpty icon={<Archive />} title="No deployment history" copy="This worker has no recorded revisions yet." /></div>
  return <table className="w-full min-w-[700px] text-left"><thead><tr className="border-b border-[#e3ded3] font-mono text-[9px]   text-[#989b95]"><th className="px-5 py-3">State</th><th>Deployment</th><th>Entrypoint</th><th>Bundle</th><th>Compatibility</th><th className="pr-5">Created</th></tr></thead><tbody>{deployments.map((deployment) => <tr key={deployment.id} className="border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/70"><td className="px-5 py-4"><Badge tone={deployment.state === "active" ? "green" : "orange"}>{deployment.state}</Badge></td><td className="max-w-52 truncate font-mono text-[10px] text-[#727a74]" title={deployment.id}>{deployment.id}</td><td className="font-mono text-[10px] text-[#727a74]">{deployment.entrypoint}</td><td className="font-mono text-[10px] text-[#727a74]">{formatBytes(deployment.bundle_size)}</td><td className="font-mono text-[10px] text-[#727a74]">{deployment.compatibility_date}</td><td className="pr-5 text-[#7d837d]">{new Date(deployment.created_at).toLocaleString()}</td></tr>)}</tbody></table>
}
