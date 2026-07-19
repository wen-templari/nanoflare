import { Card, Group, ScrollArea, SegmentedControl, SimpleGrid, Table, Text, ThemeIcon, Title } from "@mantine/core";
import { Activity, BookOpen, CalendarClock, Database, Gauge, HardDrive, Play, Rows3, Table2, Trash2, Waypoints, Workflow, type LucideIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { Bar, BarChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { apiFetch, errorText, fetchJSON } from "../app/api";
import type { DatabaseMetrics, DatabaseMetricsTimeseries, MetricPoint } from "../app/types";
import { formatBytes, sortDatabases } from "../app/utils";
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

const slashCommands = [
  { command: "/clear", description: "Clear the console screen." },
  { command: "/help", description: "Display these hints again." },
  { command: "/?", description: "Display these hints again." },
  { command: "/tables", description: "Show a list of tables in this database." },
];

const tablesSQL = "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name;";

function helpQueryRun(): QueryRun {
  return {
    id: crypto.randomUUID(),
    sql: "/help",
    createdAt: new Date().toISOString(),
    response: {
      results: [{
        success: true,
        meta: { duration: 0, changes: 0 },
        results: slashCommands.map(({ command, description }) => ({ command, description })),
      }],
    },
  };
}

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
  const [tab, setTab] = useState<"overview" | "query" | "settings">("overview");
  const [sql, setSQL] = useState("");
  const [querying, setQuerying] = useState(false);
  const [queryRuns, setQueryRuns] = useState<QueryRun[]>(() => [helpQueryRun()]);
  const [metrics, setMetrics] = useState<DatabaseMetrics>(() => emptyDatabaseMetrics());
  const [series, setSeries] = useState<DatabaseMetricsTimeseries>(() => emptyDatabaseMetricsTimeseries());
  const queryResultEndRef = useRef<HTMLDivElement>(null);
  const bindings = workers.flatMap((worker) =>
    (worker.bindings ?? [])
      .filter((binding) => binding.kind === "db" && binding.database_id === database.id)
      .map((binding) => ({ worker, binding })),
  );

  useEffect(() => {
    if (tab !== "query" || !queryRuns.length) return;
    queryResultEndRef.current?.scrollIntoView({ block: "end" });
  }, [queryRuns.length, tab]);

  useEffect(() => {
    if (!apiConnected) {
      setMetrics(emptyDatabaseMetrics());
      setSeries(emptyDatabaseMetricsTimeseries());
      return;
    }
    let cancelled = false;
    async function loadMetrics() {
      const [nextMetrics, nextSeries] = await Promise.all([
        fetchJSON<DatabaseMetrics>(`/v1/db/${encodeURIComponent(database.id)}/metrics`).catch(() => emptyDatabaseMetrics()),
        fetchJSON<DatabaseMetricsTimeseries>(`/v1/db/${encodeURIComponent(database.id)}/metrics/timeseries`).catch(() => emptyDatabaseMetricsTimeseries()),
      ]);
      if (!cancelled) setMetrics(nextMetrics);
      if (!cancelled) setSeries(nextSeries);
    }
    void loadMetrics();
    const interval = window.setInterval(() => void loadMetrics(), 15000);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [apiConnected, database.id]);

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
    const slashCommand = parseSlashCommand(trimmed);
    if (slashCommand) {
      if (await runSlashCommand(slashCommand)) setSQL("");
      return;
    }
    if (!apiConnected) return notify("API connection is required to run SQL");
    await executeSQL(trimmed, trimmed);
    setSQL("");
  }

  async function runSlashCommand(command: string) {
    switch (command) {
    case "/clear":
      setQueryRuns([]);
      return true;
    case "/help":
    case "/?":
      setQueryRuns((current) => [...current, { ...helpQueryRun(), sql: command }]);
      return true;
    case "/tables":
      if (!apiConnected) {
        notify("API connection is required to list tables");
        return false;
      }
      await executeSQL(tablesSQL, command);
      return true;
    default:
      notify(`Unknown slash command: ${command}`);
      return false;
    }
  }

  async function executeSQL(statement: string, label: string) {
    setQuerying(true);
    const run: QueryRun = { id: crypto.randomUUID(), sql: label, createdAt: new Date().toISOString() };
    try {
      const response = await apiFetch(`/v1/db/${encodeURIComponent(database.id)}/execute`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ statements: [{ sql: statement }] }),
      });
      if (!response.ok) throw new Error(await errorText(response, `Query failed (${response.status})`));
      run.response = await response.json() as DBQueryResponse;
    } catch (error) {
      run.error = error instanceof Error ? error.message : "Query failed";
    } finally {
      setQueryRuns((current) => [...current, run]);
      if (apiConnected) {
        void fetchJSON<DatabaseMetrics>(`/v1/db/${encodeURIComponent(database.id)}/metrics`).then(setMetrics).catch(() => undefined);
        void fetchJSON<DatabaseMetricsTimeseries>(`/v1/db/${encodeURIComponent(database.id)}/metrics/timeseries`).then(setSeries).catch(() => undefined);
      }
      setQuerying(false);
    }
  }

  const metricCards = [
    { label: "Total queries", value: compactNumber(metrics.queries), note: metrics.available ? "successful DB executions" : "metrics unavailable", icon: Activity },
    { label: "Rows read", value: compactNumber(metrics.rows_read), note: metrics.available ? "rows scanned or returned" : "metrics unavailable", icon: Rows3 },
    { label: "Rows written", value: compactNumber(metrics.rows_written), note: metrics.available ? "rows changed" : "metrics unavailable", icon: Rows3 },
    { label: "Storage used", value: formatBytes(metrics.storage_bytes), note: metrics.available ? "current SQLite file size" : "metrics unavailable", icon: HardDrive },
    { label: "Tables", value: compactNumber(metrics.table_count), note: metrics.available ? "current user tables" : "metrics unavailable", icon: Table2 },
  ];

  return (
    <>
      <div className="mb-6">
        <SegmentedControl
          data={[
            { label: "Overview", value: "overview" },
            { label: "Query", value: "query" },
            { label: "Settings", value: "settings" },
          ]}
          onChange={(value) => setTab(value as "overview" | "query" | "settings")}
          value={tab}
        />
      </div>

      {tab === "overview" ? (
        <>
          <SimpleGrid cols={{ base: 1, sm: 2, lg: 5 }} spacing="md">
            {metricCards.map((metric) => (
              <MetricCard key={metric.label} {...metric} />
            ))}
          </SimpleGrid>

          <div className="mt-6 grid gap-6 xl:grid-cols-2">
            <Panel title="Query mix" eyebrow="Database metrics">
              <DatabaseQueryMixChart series={series} />
            </Panel>
            <Panel title="Row activity" eyebrow="Database metrics">
              <DatabaseRowsChart series={series} />
            </Panel>
            <Panel title="Query latency histogram" eyebrow="Database metrics">
              <DatabaseLatencyHistogram metrics={metrics} />
            </Panel>
            <Panel title="Storage and schema" eyebrow="Database metrics">
              <DatabaseStorageChart series={series} />
            </Panel>
            <Panel title="Query latency" eyebrow="Database metrics">
              <DatabaseLatencySeriesChart series={series} />
            </Panel>
          </div>

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
      ) : tab === "query" ? (
        <Panel flush>
          <div className="flex h-[calc(100dvh-310px)] min-h-0 flex-col overflow-hidden bg-white">
            <div className="min-h-0 flex-1 bg-[#f8faf9]">
              <div className="h-full overflow-auto">
                <div className={queryRuns.length ? "space-y-4 p-5" : "flex min-h-full items-center justify-center p-5"}>
                  {queryRuns.length ? queryRuns.map((run, index) => (
                    <QueryRunCard key={run.id} index={index + 1} run={run} />
                  )) : (
                    <div className="max-w-sm text-center">
                      <Database className="mx-auto mb-2 size-6" />
                      <Text fw={700} size="sm">No query run yet</Text>
                      <Text c="dimmed" size="xs">Run SQL below to build a console history of statements and results.</Text>
                    </div>
                  )}
                  <div ref={queryResultEndRef} />
                </div>
              </div>
            </div>
            <div className="flex items-center gap-2 border-t border-gray-200 bg-white p-3">
              <input
                value={sql}
                onChange={(event) => setSQL(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                    event.preventDefault();
                    void runQuery();
                  }
                }}
                spellCheck={false}
                className="min-w-0 flex-1 border border-gray-300 bg-white px-2 py-1.5 font-mono text-[12px] text-gray-900 outline-none focus:border-blue-500"
              />
              <Button disabled={querying} onClick={() => void runQuery()}><Play className="size-3.5" />Run</Button>
            </div>
          </div>
        </Panel>
      ) : (
        <div className="space-y-6">
          <Panel title="Basic info" eyebrow="Database">
            <div className="overflow-hidden rounded-lg border border-[#e2ddd2]">
              {[
                ["Database ID", database.id],
                ["Name", database.name],
                ["Engine", "SQLite"],
                ["Created", new Date(database.created_at).toLocaleString()],
                ["Bindings", String(bindings.length)],
              ].map(([label, value]) => (
                <div key={label} className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]">
                  <span className="font-mono text-[10px] text-[#93978f]">{label}</span>
                  <span className="break-all font-mono text-[11px] font-bold text-[#4f5a55]">{value}</span>
                </div>
              ))}
            </div>
          </Panel>
          <Panel title="Delete database" eyebrow="Danger zone">
            <div className="grid gap-4 md:grid-cols-[1fr_auto] md:items-center">
              <Text c="dimmed" size="sm">Permanently remove this database and its stored data. Databases with active worker bindings must be unbound first.</Text>
              <Button disabled={bindings.length > 0} onClick={() => void deleteDatabase()} variant="ghost"><Trash2 className="size-4" />Delete database</Button>
            </div>
          </Panel>
        </div>
      )}
    </>
  );
}

function QueryRunCard({ index, run }: { index: number; run: QueryRun }) {
  const result = run.response?.results?.[0];
  const rows = result?.results ?? [];
  const columns = columnsForRows(rows);
  const status = run.error || resultSummary(result, run.response?.bookmark);

  return (
    <div className="overflow-hidden">
      <pre className="overflow-auto font-mono text-sm"><code>{"> " + run.sql}</code></pre>
      {run.error ? (
        <div className="px-4 py-4">
          <Text c="red" size="sm">{run.error}</Text>
        </div>
      ) : columns.length ? (
        <ScrollArea>
          <Table miw={720} verticalSpacing="sm" className="table-fixed border-collapse border border-gray-300">
            <Table.Thead>
              <Table.Tr>
                {columns.map((column) => (
                  <Table.Th key={column} className="border border-gray-300 bg-gray-50">
                    <Text ff="monospace" size="xs" truncate>{column}</Text>
                  </Table.Th>
                ))}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((row, rowIndex) => (
                <Table.Tr key={rowIndex}>
                  {columns.map((column) => (
                    <Table.Td key={column} className="border border-gray-300">
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
      <div className="border-t border-gray-200 bg-gray-50  py-2 text-xs">
        <Text c={run.error ? "red" : "dimmed"} ff="monospace" size="xs">{status}</Text>
      </div>
    </div>
  );
}

function MetricCard({ icon: Icon, label, note, value }: { icon: LucideIcon; label: string; note: string; value: string }) {
  return (
    <Card padding="md" radius="md" withBorder>
      <Group justify="space-between">
        <Text c="dimmed" fw={700} size="xs" tt="uppercase">{label}</Text>
        <ThemeIcon size="sm" variant="light"><Icon size={14} /></ThemeIcon>
      </Group>
      <Title mt="sm" order={3}>{value}</Title>
      <Text c="dimmed" size="xs">{note}</Text>
    </Card>
  );
}

function DatabaseLatencyHistogram({ metrics }: { metrics: DatabaseMetrics }) {
  const data = latencyHistogramData(metrics);
  const hasSamples = data.some((item) => item.count > 0);
  if (!hasSamples) {
    return (
      <div className="flex min-h-64 items-center justify-center rounded-lg border border-dashed border-[#d8d2c6] bg-[#fbfaf6] px-6 text-center">
        <div>
          <Gauge className="mx-auto mb-2 size-6 text-[#7b827b]" />
          <Text fw={700} size="sm">No latency samples yet</Text>
          <Text c="dimmed" size="xs">Run a query or send database traffic to populate the histogram.</Text>
        </div>
      </div>
    );
  }
  return (
    <div className="h-72 min-w-0">
      <ResponsiveContainer height="100%" width="100%">
        <BarChart data={data} margin={{ bottom: 8, left: -22, right: 8, top: 8 }}>
          <CartesianGrid stroke="#eee7dc" vertical={false} />
          <XAxis
            axisLine={false}
            dataKey="label"
            interval={0}
            tick={{ fill: "#68716b", fontSize: 10 }}
            tickLine={false}
          />
          <YAxis
            allowDecimals={false}
            axisLine={false}
            tick={{ fill: "#68716b", fontSize: 10 }}
            tickFormatter={(value) => compactNumber(Number(value))}
            tickLine={false}
          />
          <Tooltip
            contentStyle={{ border: "1px solid #ded7cb", borderRadius: 6, boxShadow: "0 10px 25px rgba(49, 56, 51, 0.08)" }}
            formatter={(value) => [compactNumber(Number(value)), "Queries"]}
            labelFormatter={(label) => `Latency ${label}`}
          />
          <Bar dataKey="count" fill="#256f6a" radius={[4, 4, 0, 0]} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}

function DatabaseQueryMixChart({ series }: { series: DatabaseMetricsTimeseries }) {
  return (
    <DatabaseTimeseriesBars
      emptyCopy="Run read or write queries while Prometheus is scraping to populate query series."
      series={[
        { key: "total", label: "Total", points: series.queries },
        { key: "read", label: "Read", points: series.read_queries },
        { key: "write", label: "Write", points: series.write_queries },
      ]}
      valueLabel="Queries"
    />
  );
}

function DatabaseRowsChart({ series }: { series: DatabaseMetricsTimeseries }) {
  return (
    <DatabaseTimeseriesBars
      emptyCopy="Run queries that read or write rows while Prometheus is scraping to populate row series."
      series={[
        { key: "read", label: "Read", points: series.rows_read },
        { key: "written", label: "Written", points: series.rows_written },
      ]}
      valueLabel="Rows"
    />
  );
}

function DatabaseStorageChart({ series }: { series: DatabaseMetricsTimeseries }) {
  return (
    <DatabaseTimeseriesBars
      emptyCopy="Create tables or write data while Prometheus is scraping to populate storage and schema series."
      series={[
        { key: "storage", label: "Storage", points: series.storage_bytes, formatter: formatBytes },
        { key: "tables", label: "Tables", points: series.table_count },
      ]}
      valueLabel="Value"
    />
  );
}

function DatabaseLatencySeriesChart({ series }: { series: DatabaseMetricsTimeseries }) {
  return (
    <DatabaseTimeseriesLines
      emptyCopy="Run queries while Prometheus is scraping to populate latency percentiles."
      series={[
        { key: "p50", label: "P50", points: series.p50_latency_ms, formatter: formatQueryDuration },
        { key: "p95", label: "P95", points: series.p95_latency_ms, formatter: formatQueryDuration },
        { key: "p99", label: "P99", points: series.p99_latency_ms, formatter: formatQueryDuration },
      ]}
      valueLabel="Latency"
    />
  );
}

type DatabaseSeries = {
  key: string;
  label: string;
  points: MetricPoint[];
  formatter?: (value: number) => string;
};

function DatabaseTimeseriesBars({
  emptyCopy,
  series,
  valueLabel,
}: {
  emptyCopy: string;
  series: DatabaseSeries[];
  valueLabel: string;
}) {
  const data = mergeTimeseries(series);
  const hasSamples = data.some((item) => series.some((entry) => Number(item[entry.key] ?? 0) > 0));
  if (!hasSamples) {
    return <DatabaseChartEmpty copy={emptyCopy} />;
  }
  return (
    <div className="h-72 min-w-0">
      <ResponsiveContainer height="100%" width="100%">
        <BarChart data={data} margin={{ bottom: 8, left: -18, right: 8, top: 8 }}>
          <CartesianGrid stroke="#eee7dc" vertical={false} />
          <XAxis
            axisLine={false}
            dataKey="timestamp"
            tick={{ fill: "#68716b", fontSize: 11 }}
            tickFormatter={formatSeriesTick}
            tickLine={false}
          />
          <YAxis
            allowDecimals={false}
            axisLine={false}
            tick={{ fill: "#68716b", fontSize: 10 }}
            tickFormatter={(value) => compactNumber(Number(value))}
            tickLine={false}
          />
          <Tooltip
            contentStyle={{ border: "1px solid #ded7cb", borderRadius: 6, boxShadow: "0 10px 25px rgba(49, 56, 51, 0.08)" }}
            formatter={(value, name) => [formatSeriesValue(series, String(name), Number(value)), valueLabel]}
            labelFormatter={formatSeriesLabel}
          />
          {series.map((entry, index) => (
            <Bar key={entry.key} dataKey={entry.key} fill={chartColors[index % chartColors.length]} name={entry.key} radius={[4, 4, 0, 0]} />
          ))}
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}

function DatabaseTimeseriesLines({
  emptyCopy,
  series,
  valueLabel,
}: {
  emptyCopy: string;
  series: DatabaseSeries[];
  valueLabel: string;
}) {
  const data = mergeTimeseries(series);
  const hasSamples = data.some((item) => series.some((entry) => Number(item[entry.key] ?? 0) > 0));
  if (!hasSamples) {
    return <DatabaseChartEmpty copy={emptyCopy} />;
  }
  return (
    <div className="h-72 min-w-0">
      <ResponsiveContainer height="100%" width="100%">
        <LineChart data={data} margin={{ bottom: 8, left: -18, right: 8, top: 8 }}>
          <CartesianGrid stroke="#eee7dc" vertical={false} />
          <XAxis
            axisLine={false}
            dataKey="timestamp"
            tick={{ fill: "#68716b", fontSize: 11 }}
            tickFormatter={formatSeriesTick}
            tickLine={false}
          />
          <YAxis
            axisLine={false}
            tick={{ fill: "#68716b", fontSize: 10 }}
            tickFormatter={(value) => formatQueryDuration(Number(value))}
            tickLine={false}
          />
          <Tooltip
            contentStyle={{ border: "1px solid #ded7cb", borderRadius: 6, boxShadow: "0 10px 25px rgba(49, 56, 51, 0.08)" }}
            formatter={(value, name) => [formatSeriesValue(series, String(name), Number(value)), valueLabel]}
            labelFormatter={formatSeriesLabel}
          />
          {series.map((entry, index) => (
            <Line key={entry.key} dataKey={entry.key} dot={false} name={entry.key} stroke={chartColors[index % chartColors.length]} strokeWidth={2} type="monotone" />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

function DatabaseChartEmpty({ copy }: { copy: string }) {
  return (
    <div className="flex min-h-64 items-center justify-center rounded-lg border border-dashed border-[#d8d2c6] bg-[#fbfaf6] px-6 text-center">
      <div>
        <Gauge className="mx-auto mb-2 size-6 text-[#7b827b]" />
        <Text fw={700} size="sm">No chart data yet</Text>
        <Text c="dimmed" size="xs">{copy}</Text>
      </div>
    </div>
  );
}

function parseSlashCommand(input: string) {
  if (!input.startsWith("/")) return "";
  const command = input.split(/\s+/, 1)[0].toLowerCase();
  return command;
}

function columnsForRows(rows: Record<string, unknown>[]) {
  const seen = new Set<string>();
  for (const row of rows) {
    for (const key of Object.keys(row)) seen.add(key);
  }
  return [...seen];
}

const chartColors = ["#256f6a", "#3b82f6", "#9b8cf2", "#d97706"];

function mergeTimeseries(series: DatabaseSeries[]) {
  const rows = new Map<string, Record<string, number | string>>();
  for (const entry of series) {
    for (const point of entry.points ?? []) {
      const timestamp = point.timestamp;
      if (!timestamp) continue;
      const row = rows.get(timestamp) ?? { timestamp };
      row[entry.key] = point.value;
      rows.set(timestamp, row);
    }
  }
  return [...rows.values()].sort((a, b) => new Date(String(a.timestamp)).getTime() - new Date(String(b.timestamp)).getTime());
}

function formatSeriesValue(series: DatabaseSeries[], key: string, value: number) {
  const entry = series.find((item) => item.key === key);
  if (entry?.formatter) return entry.formatter(value);
  return compactNumber(value);
}

function formatSeriesTick(timestamp: string) {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function formatSeriesLabel(timestamp: unknown) {
  const raw = String(timestamp ?? "");
  const date = new Date(raw);
  if (Number.isNaN(date.getTime())) return raw;
  return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function latencyHistogramData(metrics: DatabaseMetrics) {
  return [
    { label: "<=0.5ms", count: metrics.duration_bucket_0_5 },
    { label: "<=1ms", count: metrics.duration_bucket_1 },
    { label: "<=2.5ms", count: metrics.duration_bucket_2_5 },
    { label: "<=5ms", count: metrics.duration_bucket_5 },
    { label: "<=10ms", count: metrics.duration_bucket_10 },
    { label: "<=25ms", count: metrics.duration_bucket_25 },
    { label: "<=50ms", count: metrics.duration_bucket_50 },
    { label: "<=100ms", count: metrics.duration_bucket_100 },
    { label: "<=250ms", count: metrics.duration_bucket_250 },
    { label: "<=500ms", count: metrics.duration_bucket_500 },
    { label: "<=1s", count: metrics.duration_bucket_1000 },
    { label: ">1s", count: metrics.duration_bucket_inf },
  ];
}

function emptyDatabaseMetricsTimeseries(): DatabaseMetricsTimeseries {
  return {
    available: false,
    queries: [],
    read_queries: [],
    write_queries: [],
    rows_read: [],
    rows_written: [],
    storage_bytes: [],
    table_count: [],
    p50_latency_ms: [],
    p95_latency_ms: [],
    p99_latency_ms: [],
  };
}

function emptyDatabaseMetrics(): DatabaseMetrics {
  return {
    available: false,
    queries: 0,
    read_queries: 0,
    write_queries: 0,
    rows_read: 0,
    rows_returned: 0,
    rows_written: 0,
    storage_bytes: 0,
    table_count: 0,
    total_duration_ms: 0,
    p50_duration_ms: 0,
    p99_duration_ms: 0,
    duration_bucket_0_5: 0,
    duration_bucket_1: 0,
    duration_bucket_2_5: 0,
    duration_bucket_5: 0,
    duration_bucket_10: 0,
    duration_bucket_25: 0,
    duration_bucket_50: 0,
    duration_bucket_100: 0,
    duration_bucket_250: 0,
    duration_bucket_500: 0,
    duration_bucket_1000: 0,
    duration_bucket_inf: 0,
  };
}

function compactNumber(value: number) {
  if (!Number.isFinite(value)) return "0";
  return Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function resultSummary(result?: D1Result, bookmark?: string) {
  if (!result) return "Run SQL to see rows, changes, and execution metadata.";
  const meta = result.meta ?? {};
  const parts = [
    `${result.results?.length ?? 0} row${(result.results?.length ?? 0) === 1 ? "" : "s"}`,
    `${meta.changes ?? 0} change${(meta.changes ?? 0) === 1 ? "" : "s"}`,
    formatQueryDuration(meta.duration ?? 0),
  ];
  if (bookmark) parts.push(`bookmark ${bookmark}`);
  return parts.join(" / ");
}

function formatQueryDuration(duration: number) {
  if (!Number.isFinite(duration) || duration <= 0) return "0ms";
  if (duration < 1) return `${duration.toFixed(2)}ms`;
  if (duration < 10) return `${duration.toFixed(1)}ms`;
  return `${Math.round(duration)}ms`;
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
