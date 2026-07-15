import { Center, ScrollArea, Stack, Table, Text } from "@mantine/core";
import { DatabaseZap, Plus } from "lucide-react";
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
      <PageHeading eyebrow="Storage" title="Object storage" copy="Manage bucket inventory for your workers." actions={<Button onClick={openObjectStorageBucketDialog}><Plus className="size-4" />New bucket</Button>} />
      <Panel flush>
        <ScrollArea>
          <Table highlightOnHover miw={760} verticalSpacing="sm" className="table-fixed">
            <Table.Thead><Table.Tr><Table.Th className="w-[30%]">Bucket</Table.Th><Table.Th className="w-[44%]">ID</Table.Th><Table.Th className="w-[14%]">Bindings</Table.Th><Table.Th className="w-[12%]">Created</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {objectStorageBuckets.map((bucket) => {
                const boundCount = workers.filter((worker) => worker.bindings?.some((binding) => binding.kind === "object_storage_bucket" && binding.bucket_id === bucket.id)).length;
                return (
                  <Table.Tr key={bucket.id} className="cursor-pointer" onClick={() => navigate(`/object-storage/${bucket.id}`)}>
                    <Table.Td className="w-[30%]"><Text fw={700} truncate>{bucket.name}</Text></Table.Td>
                    <Table.Td className="w-[44%]"><Text c="dimmed" ff="monospace" size="xs" truncate>{bucket.id}</Text></Table.Td>
                    <Table.Td className="w-[14%]"><Badge tone={boundCount ? "green" : "orange"}>{boundCount} worker{boundCount === 1 ? "" : "s"}</Badge></Table.Td>
                    <Table.Td className="w-[12%]"><Text c="dimmed" size="sm" truncate>{new Date(bucket.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Text></Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </ScrollArea>
        {!objectStorageBuckets.length && <Center h={240}><Stack align="center" gap={4}><DatabaseZap size={22} /><Text fw={700} size="sm">No buckets yet</Text><Text c="dimmed" size="xs">Create one to bind object storage into a worker</Text></Stack></Center>}
      </Panel>
    </>
  );
}
