import { Card, Group, ScrollArea, SegmentedControl, SimpleGrid, Table, Text, ThemeIcon, Title } from "@mantine/core";
import { CalendarClock, Database, Play, Table2, Trash2, Waypoints, Workflow } from "lucide-react";
import { useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { apiFetch, errorText } from "../app/api";
import { sortDatabases } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { Field, Panel, WorkerDetailEmpty } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

type D1Meta = {
  duration?: number;
  changes?: number;
  last_row_id?: number;
  changed_db?: boolean;
  rows_read?: number;
  rows_written?: number;
};

type D1Result = {
  success: boolean;
  meta?: D1Meta;
  results?: Record<string, unknown>[];
};

type DBQueryResponse = {
  results?: D1Result[];
  bookmark?: string;
};

type QueryRun = {
  id: string;
  sql: string;
  createdAt: string;
  response?: DBQueryResponse;
  error?: string;
};

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
  const [tab, setTab] = useState<"overview" | "query">("overview");
  const [sql, setSQL] = useState("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name;");
  const [querying, setQuerying] = useState(false);
  const [queryRuns, setQueryRuns] = useState<QueryRun[]>([]);
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

  async function runQuery() {
    const trimmed = sql.trim();
    if (!trimmed) return notify("SQL is required");
    setQuerying(true);
    const run: QueryRun = { id: crypto.randomUUID(), sql: trimmed, createdAt: new Date().toISOString() };
    try {
      const response = await apiFetch(`/v1/db/${encodeURIComponent(database.id)}/execute`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ statements: [{ sql: trimmed }] }),
      });
      if (!response.ok) throw new Error(await errorText(response, `Query failed (${response.status})`));
      run.response = await response.json() as DBQueryResponse;
    } catch (error) {
      run.error = error instanceof Error ? error.message : "Query failed";
    } finally {
      setQueryRuns((current) => [...current, run]);
      setQuerying(false);
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

      <div className="mb-6">
        <SegmentedControl
          data={[
            { label: "Overview", value: "overview" },
            { label: "Query", value: "query" },
          ]}
          onChange={(value) => setTab(value as "overview" | "query")}
          value={tab}
        />
      </div>

      {tab === "overview" ? (
        <>
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
      ) : (
        <Panel flush>
          <div className="flex min-h-[680px] flex-col bg-white">
            <div className="min-h-0 flex-1 bg-[#f8faf9]">
              <ScrollArea h={520}>
                <div className="space-y-4 p-5">
                  {queryRuns.length ? queryRuns.map((run, index) => (
                    <QueryRunCard key={run.id} index={index + 1} run={run} />
                  )) : (
                    <div className="px-5 py-16">
                      <WorkerDetailEmpty icon={<Database />} title="No query run yet" copy="Run SQL below to build a console history of statements and results." />
                    </div>
                  )}
                </div>
              </ScrollArea>
            </div>
            <div className="border-t border-gray-200 bg-[#202b29]">
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/10 px-4 py-3">
                <div className="flex items-center gap-2 font-mono text-[10px] font-bold text-[#b5c1bb]"><Database className="size-3.5 text-cyan-300" />sql console</div>
                <Button disabled={!apiConnected || querying} onClick={() => void runQuery()}><Play className="size-3.5" />Run</Button>
              </div>
              <textarea
                value={sql}
                onChange={(event) => setSQL(event.target.value)}
                spellCheck={false}
                className="min-h-36 w-full resize-y bg-transparent p-4 font-mono text-[12px] leading-6 text-[#d8dfd8] outline-none"
              />
            </div>
          </div>
        </Panel>
      )}
    </>
  );
}

function QueryRunCard({ index, run }: { index: number; run: QueryRun }) {
  const result = run.response?.results?.[0];
  const rows = result?.results ?? [];
  const columns = columnsForRows(rows);

  return (
    <div className="overflow-hidden rounded-lg border border-gray-200 bg-white shadow-sm">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 px-4 py-3">
        <div className="min-w-0">
          <Text c="dimmed" ff="monospace" size="xs">Query {index} / {new Date(run.createdAt).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" })}</Text>
          <Text c={run.error ? "red" : "dimmed"} size="sm">{run.error || resultSummary(result, run.response?.bookmark)}</Text>
        </div>
        <ThemeIcon color={run.error ? "red" : "blue"} size="lg" variant="light"><Table2 size={16} /></ThemeIcon>
      </div>
      <pre className="max-h-44 overflow-auto border-b border-gray-200 bg-[#202b29] p-4 font-mono text-[11px] leading-6 text-[#d8dfd8]"><code>{run.sql}</code></pre>
      {run.error ? (
        <div className="px-4 py-4">
          <Text c="red" size="sm">{run.error}</Text>
        </div>
      ) : columns.length ? (
        <ScrollArea>
          <Table miw={720} verticalSpacing="sm" className="table-fixed">
            <Table.Thead>
              <Table.Tr>
                {columns.map((column) => <Table.Th key={column}><Text ff="monospace" size="xs" truncate>{column}</Text></Table.Th>)}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((row, rowIndex) => (
                <Table.Tr key={rowIndex}>
                  {columns.map((column) => (
                    <Table.Td key={column}>
                      <Text ff="monospace" size="xs" truncate title={formatCell(row[column])}>{formatCell(row[column])}</Text>
                    </Table.Td>
                  ))}
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </ScrollArea>
      ) : (
        <div className="px-4 py-5">
          <Text c="dimmed" size="sm">Statement complete. No rows returned.</Text>
        </div>
      )}
    </div>
  );
}

function columnsForRows(rows: Record<string, unknown>[]) {
  const seen = new Set<string>();
  for (const row of rows) {
    for (const key of Object.keys(row)) seen.add(key);
  }
  return [...seen];
}

function resultSummary(result?: D1Result, bookmark?: string) {
  if (!result) return "Run SQL to see rows, changes, and execution metadata.";
  const meta = result.meta ?? {};
  const parts = [
    `${result.results?.length ?? 0} row${(result.results?.length ?? 0) === 1 ? "" : "s"}`,
    `${meta.changes ?? 0} change${(meta.changes ?? 0) === 1 ? "" : "s"}`,
    `${meta.duration ?? 0}ms`,
  ];
  if (bookmark) parts.push(`bookmark ${bookmark}`);
  return parts.join(" / ");
}

function formatCell(value: unknown) {
  if (value === null || value === undefined) return "NULL";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
