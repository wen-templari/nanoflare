import { ChevronRight, Plus, Trash2 } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useWorkspace } from "../app/workspace-context";
import type { Worker } from "../app/types";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function WorkersPage() {
  const navigate = useNavigate();
  const { workers, setWorkers, openWorkerDialog, notify, apiConnected } = useWorkspace();

  return (
    <>
      <PageHeading eyebrow="Runtime" title="Workers" copy="Register isolated services, deploy bundles, and watch the runtime pool." actions={<Button onClick={openWorkerDialog}><Plus className="size-4" />New worker</Button>} />
      <Panel title={`${workers.length} registered workers`} eyebrow={apiConnected ? "Live inventory" : "Demo inventory"} flush>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[720px] text-left">
            <thead><tr className="border-b border-[#e3ded3] font-mono text-[9px] uppercase tracking-[0.14em] text-[#989b95]"><th className="px-5 py-3">Worker</th><th>State</th><th>Requests</th><th>Deployment</th><th>Created</th><th className="pr-4 text-right">Actions</th></tr></thead>
            <tbody>{workers.map((worker) => <WorkerRow key={worker.id} worker={worker} workers={workers} setWorkers={(nextWorkers) => setWorkers(nextWorkers)} notify={notify} onSelect={() => navigate(`/workers/${worker.id}`)} />)}</tbody>
          </table>
        </div>
      </Panel>
    </>
  );
}

function WorkerRow({ worker, workers, setWorkers, notify, onSelect }: { worker: Worker; workers: Worker[]; setWorkers: (workers: Worker[]) => void; notify: (text: string) => void; onSelect: () => void }) {
  return (
    <tr className="cursor-pointer border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/70" onClick={onSelect}>
      <td className="px-5 py-4"><div className="flex items-center gap-3"><div><p className="font-extrabold text-[#35413e]">{worker.name}</p><p className="mt-1 font-mono text-[10px] text-[#949891]">{worker.hostname}</p></div><ChevronRight className="size-3.5 text-[#c0beb6] transition" /></div></td>
      <td><Badge tone={worker.status === "draft" ? "orange" : "green"}>{worker.status ?? "live"}</Badge></td>
      <td className="font-mono text-[11px]">{worker.requests ?? "0"}</td>
      <td className="font-mono text-[10px] text-[#727a74]">{worker.deployment ?? "awaiting deploy"}</td>
      <td className="text-[#7d837d]">{new Date(worker.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</td>
      <td className="pr-4 text-right"><Button variant="ghost" size="icon" aria-label={`Delete ${worker.name}`} onClick={(event) => { event.stopPropagation(); setWorkers(workers.filter(({ id }) => id !== worker.id)); notify(`${worker.name} removed`); }}><Trash2 className="size-3.5" /></Button></td>
    </tr>
  );
}
