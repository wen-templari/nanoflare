import { ChevronRight, KeyRound, Plus } from "lucide-react"
import { useNavigate } from "react-router-dom"
import { useWorkspace } from "../app/workspace-context"
import { PageHeading, Panel } from "../components/shared/primitives"
import { Badge } from "../components/ui/badge"
import { Button } from "../components/ui/button"

export function KVNamespacesPage() {
  const navigate = useNavigate()
  const { namespaces, workers, openNamespaceDialog } = useWorkspace()

  return (
    <>
      <PageHeading eyebrow="Storage" title="KV" copy="Manage namespace inventory for your workers, then drill into a namespace to rename it or inspect its shared data." actions={<Button onClick={openNamespaceDialog}><Plus className="size-4" />New namespace</Button>} />
      <Panel flush>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[760px] text-left">
            <thead><tr className="border-b border-[#e3ded3] font-mono text-[9px]   text-[#989b95]"><th className="px-5 py-3">Namespace</th><th>ID</th><th>Bindings</th><th>Created</th><th className="pr-5 text-right">Open</th></tr></thead>
            <tbody>
              {namespaces.map((namespace) => {
                const boundCount = workers.filter((worker) => worker.bindings?.some((binding) => binding.kind === "kv" && binding.namespace_id === namespace.id)).length
                return (
                  <tr key={namespace.id} className="cursor-pointer border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/70" onClick={() => navigate(`/kv/${namespace.id}`)}>
                    <td className="px-5 py-4">
                      <div>
                        <p className="font-extrabold text-[#35413e]">{namespace.name}</p>
                      </div>
                    </td>
                    <td className="font-mono text-[10px] text-[#727a74]">{namespace.id}</td>
                    <td><Badge tone={boundCount ? "green" : "orange"}>{boundCount} worker{boundCount === 1 ? "" : "s"}</Badge></td>
                    <td className="text-[#7d837d]">{new Date(namespace.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</td>
                    <td className="pr-5 text-right text-[#c0beb6]"><ChevronRight className="ml-auto size-3.5" /></td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        {!namespaces.length && <div className="grid h-60 place-items-center text-center"><div><KeyRound className="mx-auto size-5 text-[#b7b4ac]" /><p className="mt-3 text-xs font-extrabold text-[#777e78]">No namespaces yet</p><p className="mt-1 font-mono text-[9px]   text-[#a1a49e]">Create one to bind KV storage into a worker</p></div></div>}
      </Panel>
    </>
  )
}
