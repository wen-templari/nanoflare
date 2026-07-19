import { Card, Group, SimpleGrid, Text, ThemeIcon, Title } from "@mantine/core";
import { CalendarClock, Database, Trash2, Waypoints, Workflow } from "lucide-react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { apiFetch, errorText } from "../app/api";
import { sortDatabases } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { Field, Panel, WorkerDetailEmpty } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function DatabaseDetailPage() {
  const navigate = useNavigate();
  const { databaseId } = useParams();
  const { databases } = useWorkspace();
  const database = databases.find((item) => item.id === databaseId);

  if (!database) return <Navigate to="/databases" replace />;

  return <DatabaseDetailContent database={database} onBack={() => navigate("/databases")} />;
}

function DatabaseDetailContent({
  database,
  onBack,
}: {
  database: { id: string; name: string; created_at: string };
  onBack: () => void;
}) {
  const { apiConnected, notify, setDatabases, workers } = useWorkspace();
  const bindings = workers.flatMap((worker) =>
    (worker.bindings ?? [])
      .filter((binding) => binding.kind === "db" && binding.database_id === database.id)
      .map((binding) => ({ worker, binding })),
  );

  async function deleteDatabase() {
    if (bindings.length) return notify("Remove worker bindings before deleting this database");
    if (!window.confirm(`Delete database "${database.name}"?`)) return;
    try {
      if (apiConnected) {
        const response = await apiFetch(`/v1/db/${encodeURIComponent(database.id)}`, { method: "DELETE" });
        if (!response.ok) throw new Error(await errorText(response, `Database delete failed (${response.status})`));
      }
      setDatabases((current) => sortDatabases(current.filter((item) => item.id !== database.id)));
      notify(`${database.name} deleted`);
      onBack();
    } catch (error) {
      notify(error instanceof Error ? error.message : "Database delete failed");
    }
  }

  return (
    <>
      <div className="mb-6 flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div>
          <p className="font-mono text-xs font-bold uppercase tracking-wide text-gray-500">Database</p>
          <Title mt={4} order={1}>{database.name}</Title>
          <Text c="dimmed" ff="monospace" size="sm" mt={6}>{database.id}</Text>
        </div>
        <Button disabled={bindings.length > 0} onClick={() => void deleteDatabase()} variant="ghost"><Trash2 className="size-4" />Delete</Button>
      </div>

      <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="md">
        <Card padding="md" radius="md" withBorder>
          <Group justify="space-between">
            <Text c="dimmed" fw={700} size="xs" tt="uppercase">Bindings</Text>
            <ThemeIcon size="sm" variant="light"><Workflow size={14} /></ThemeIcon>
          </Group>
          <Title mt="sm" order={3}>{bindings.length}</Title>
          <Text c="dimmed" size="xs">worker DB bindings</Text>
        </Card>
        <Card padding="md" radius="md" withBorder>
          <Group justify="space-between">
            <Text c="dimmed" fw={700} size="xs" tt="uppercase">Engine</Text>
            <ThemeIcon size="sm" variant="light"><Database size={14} /></ThemeIcon>
          </Group>
          <Title mt="sm" order={3}>SQLite</Title>
          <Text c="dimmed" size="xs">single-node primary</Text>
        </Card>
        <Card padding="md" radius="md" withBorder>
          <Group justify="space-between">
            <Text c="dimmed" fw={700} size="xs" tt="uppercase">Created</Text>
            <ThemeIcon size="sm" variant="light"><CalendarClock size={14} /></ThemeIcon>
          </Group>
          <Title mt="sm" order={3}>{new Date(database.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Title>
          <Text c="dimmed" size="xs">{new Date(database.created_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}</Text>
        </Card>
      </SimpleGrid>

      <div className="mt-6">
        <Panel flush>
          <div className="border-b border-[#e8e3d9] px-5 py-4">
            <Field label="Bound workers">
              <Text c="dimmed" size="sm">Workers that can access this database through an active deployment.</Text>
            </Field>
          </div>
          {bindings.length ? (
            <div className="divide-y divide-gray-200">
              {bindings.map(({ worker, binding }) => (
                <div key={`${worker.id}-${binding.binding}`} className="flex items-center justify-between gap-4 px-5 py-4">
                  <div className="min-w-0">
                    <Text fw={700} truncate>{worker.name}</Text>
                    <Text c="dimmed" ff="monospace" size="xs" truncate>{binding.binding}</Text>
                  </div>
                  <Badge tone="green"><Waypoints className="mr-1 inline size-3" />Bound</Badge>
                </div>
              ))}
            </div>
          ) : (
            <div className="px-5 py-12">
              <WorkerDetailEmpty icon={<Database />} title="No worker bindings" copy="Add this database to a worker's db config and deploy it." />
            </div>
          )}
        </Panel>
      </div>
    </>
  );
}
