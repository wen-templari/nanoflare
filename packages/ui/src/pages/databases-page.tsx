import { Center, ScrollArea, Stack, Table, Text } from "@mantine/core";
import { Database, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function DatabasesPage() {
  const navigate = useNavigate();
  const { databases, workers, openDatabaseDialog } = useWorkspace();

  return (
    <>
      <PageHeading
        eyebrow="Storage"
        title="Databases"
        copy="Manage SQLite databases for Worker DB bindings."
        actions={<Button onClick={openDatabaseDialog}><Plus className="size-4" />New database</Button>}
      />
      <Panel flush>
        <ScrollArea>
          <Table highlightOnHover miw={760} verticalSpacing="sm" className="table-fixed">
            <Table.Thead><Table.Tr><Table.Th className="w-[30%]">Database</Table.Th><Table.Th className="w-[44%]">ID</Table.Th><Table.Th className="w-[14%]">Bindings</Table.Th><Table.Th className="w-[12%]">Created</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {databases.map((database) => {
                const boundCount = workers.filter((worker) => worker.bindings?.some((binding) => binding.kind === "db" && binding.database_id === database.id)).length;
                return (
                  <Table.Tr key={database.id} className="cursor-pointer" onClick={() => navigate(`/databases/${database.id}`)}>
                    <Table.Td className="w-[30%]"><Text fw={700} truncate>{database.name}</Text></Table.Td>
                    <Table.Td className="w-[44%]"><Text c="dimmed" ff="monospace" size="xs" truncate>{database.id}</Text></Table.Td>
                    <Table.Td className="w-[14%]"><Badge tone={boundCount ? "green" : "orange"}>{boundCount} worker{boundCount === 1 ? "" : "s"}</Badge></Table.Td>
                    <Table.Td className="w-[12%]"><Text c="dimmed" size="sm" truncate>{new Date(database.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Text></Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </ScrollArea>
        {!databases.length && <Center h={240}><Stack align="center" gap={4}><Database size={22} /><Text fw={700} size="sm">No databases yet</Text><Text c="dimmed" size="xs">Create one to bind SQLite into a worker</Text></Stack></Center>}
      </Panel>
    </>
  );
}
