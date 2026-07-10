import { Anchor, Group, ScrollArea, Table, Text } from "@mantine/core";
import { ChevronRight, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useWorkspace } from "../app/workspace-context";
import type { Worker } from "../app/types";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function WorkersPage() {
  const navigate = useNavigate();
  const { workers, openWorkerDialog } = useWorkspace();

  return (
    <>
      <PageHeading eyebrow="Runtime" title="Workers" copy="Register isolated services, deploy bundles, and watch the runtime pool." actions={<Button onClick={openWorkerDialog}><Plus className="size-4" />New worker</Button>} />
      <Panel flush>
        <ScrollArea>
          <Table highlightOnHover miw={720} verticalSpacing="sm">
            <Table.Thead><Table.Tr><Table.Th>Worker</Table.Th><Table.Th>State</Table.Th><Table.Th>Requests</Table.Th><Table.Th>Deployment</Table.Th><Table.Th>Created</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>{workers.map((worker) => <WorkerRow key={worker.id} worker={worker} onSelect={() => navigate(`/workers/${worker.id}`)} />)}</Table.Tbody>
          </Table>
        </ScrollArea>
      </Panel>
    </>
  );
}

function WorkerRow({ worker, onSelect }: { worker: Worker; onSelect: () => void }) {
  return (
    <Table.Tr className="cursor-pointer" onClick={onSelect}>
      <Table.Td><Group gap="sm"><div><Text fw={700}>{worker.name}</Text><Anchor ff="monospace" href={hostnameHref(worker.hostname)} onClick={(event) => event.stopPropagation()} size="xs" target="_blank">{worker.hostname}</Anchor></div></Group></Table.Td>
      <Table.Td><Badge tone={worker.status === "draft" ? "orange" : "green"}>{worker.status ?? "live"}</Badge></Table.Td>
      <Table.Td><Text ff="monospace" size="xs">{worker.requests ?? "0"}</Text></Table.Td>
      <Table.Td><Text c="dimmed" ff="monospace" size="xs">{worker.deployment ?? "awaiting deploy"}</Text></Table.Td>
      <Table.Td><Text c="dimmed" size="sm">{new Date(worker.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Text></Table.Td>
    </Table.Tr>
  );
}

function hostnameHref(hostname: string) {
  return /^https?:\/\//i.test(hostname) ? hostname : `https://${hostname}`;
}
