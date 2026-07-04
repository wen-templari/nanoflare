import { ChevronRight, DatabaseZap, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function ObjectStorageBucketsPage() {
  const navigate = useNavigate();
  const { objectStorageBuckets, workers, openObjectStorageBucketDialog } = useWorkspace();

  return (
    <>
      <PageHeading eyebrow="Storage" title="Object storage" copy="Manage bucket inventory for your workers, then drill into a bucket to inspect shared objects and bindings." actions={<Button onClick={openObjectStorageBucketDialog}><Plus className="size-4" />New bucket</Button>} />
      <Panel flush>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[760px] text-left">
            <thead><tr className="border-b border-[#e3ded3] font-mono text-[9px]   text-[#989b95]"><th className="px-5 py-3">Bucket</th><th>ID</th><th>Bindings</th><th>Created</th><th className="pr-5 text-right">Open</th></tr></thead>
            <tbody>
              {objectStorageBuckets.map((bucket) => {
                const boundCount = workers.filter((worker) => worker.bindings?.some((binding) => binding.kind === "object_storage_bucket" && binding.bucket_id === bucket.id)).length;
                return (
                  <tr key={bucket.id} className="cursor-pointer border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/70" onClick={() => navigate(`/object-storage/${bucket.id}`)}>
                    <td className="px-5 py-4"><p className="font-extrabold text-[#35413e]">{bucket.name}</p></td>
                    <td className="font-mono text-[10px] text-[#727a74]">{bucket.id}</td>
                    <td><Badge tone={boundCount ? "green" : "orange"}>{boundCount} worker{boundCount === 1 ? "" : "s"}</Badge></td>
                    <td className="text-[#7d837d]">{new Date(bucket.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</td>
                    <td className="pr-5 text-right text-[#c0beb6]"><ChevronRight className="ml-auto size-3.5" /></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {!objectStorageBuckets.length && <div className="grid h-60 place-items-center text-center"><div><DatabaseZap className="mx-auto size-5 text-[#b7b4ac]" /><p className="mt-3 text-xs font-extrabold text-[#777e78]">No buckets yet</p><p className="mt-1 font-mono text-[9px]   text-[#a1a49e]">Create one to bind object storage into a worker</p></div></div>}
      </Panel>
    </>
  );
}
