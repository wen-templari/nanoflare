import type { components } from "@nanoflare/schema";

import {
  Badge,
  Button,
  Chart,
  ChartPalette,
  LayerCard,
  Table,
  Tabs,
  Text,
  TimeseriesChart,
} from "@cloudflare/kumo";
import {
  Activity,
  Database,
  Gauge,
  HardDrive,
  Play,
  Rows3,
  Table2,
  Trash2,
  Waypoints,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";

import type { DatabaseMetrics, DatabaseMetricsTimeseries, MetricPoint } from "../app/types";

import { apiClient, errorMessage } from "../app/api";
import { useQueryTab } from "../app/use-query-tab";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { formatBytes, sortDatabases } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { ConfirmDeleteDialog } from "../components/kumo/confirm-delete-dialog";
import { Field, WorkerDetailEmpty } from "../components/shared/primitives";
import { echarts } from "../lib/kumo-echarts";

type DBQueryResponse = components["schemas"]["DBQueryResponse"];
type D1Result = components["schemas"]["D1Result"];

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
const databaseDetailTabs = ["overview", "query", "settings"] as const;

function helpQueryRun(): QueryRun {
  return {
    id: crypto.randomUUID(),
    sql: "/help",
    createdAt: new Date().toISOString(),
    response: {
      results: [
        {
          success: true,
          meta: {
            changed_db: false,
            changes: 0,
            duration: 0,
            last_row_id: 0,
            rows_read: 0,
            rows_written: 0,
            served_by: "",
            served_by_primary: false,
            size_after: 0,
          },
          results: slashCommands.map(({ command, description }) => ({ command, description })),
        },
      ],
    },
  };
}

export function DatabaseDetailPage() {
  const navigate = useNavigate();
  const { databaseId } = useParams();
  const { databases } = useWorkspace();
  const resourcesReady = useWorkspaceResources(["databases", "workers"], "details");
  const database = databases.find((item) => item.id === databaseId);

  if (!resourcesReady) return null;
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
  const { activeOrgID, apiConnected, notify, setDatabases, workers } = useWorkspace();
  const [tab, setTab] = useQueryTab<(typeof databaseDetailTabs)[number]>(
    databaseDetailTabs,
    "overview",
  );
  const [sql, setSQL] = useState("");
  const [querying, setQuerying] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [queryRuns, setQueryRuns] = useState<QueryRun[]>(() => [helpQueryRun()]);
  const [metrics, setMetrics] = useState<DatabaseMetrics>(() => emptyDatabaseMetrics());
  const [series, setSeries] = useState<DatabaseMetricsTimeseries>(() =>
    emptyDatabaseMetricsTimeseries(),
  );
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
      const nextSeries = await apiClient
        .GET("/v1/organizations/{orgID}/databases/{databaseID}/analytics", {
          params: { path: { orgID: activeOrgID, databaseID: database.id } },
          parseAs: "json",
        })
        .then(({ data }) => data ?? emptyDatabaseMetricsTimeseries());
      if (!cancelled) {
        setSeries(nextSeries);
        setMetrics(databaseMetricsFromSeries(nextSeries));
      }
    }
    void loadMetrics();
    return () => {
      cancelled = true;
    };
  }, [activeOrgID, apiConnected, database.id]);

  async function deleteDatabase() {
    if (bindings.length) return notify("Remove worker bindings before deleting this database");
    setDeleting(true);
    setDeleteError("");
    try {
      if (apiConnected) {
        const { error } = await apiClient.DELETE(
          "/v1/organizations/{orgID}/databases/{databaseID}",
          { params: { path: { orgID: activeOrgID, databaseID: database.id } }, parseAs: "json" },
        );
        if (error) throw new Error(errorMessage(error, "Database delete failed"));
      }
      setDatabases((current) => sortDatabases(current.filter((item) => item.id !== database.id)));
      notify(`${database.name} deleted`);
      onBack();
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : "Database delete failed");
    } finally {
      setDeleting(false);
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
    const run: QueryRun = {
      id: crypto.randomUUID(),
      sql: label,
      createdAt: new Date().toISOString(),
    };
    try {
      const { data, error } = await apiClient.POST(
        "/v1/organizations/{orgID}/databases/{databaseID}/queries",
        {
          params: { path: { orgID: activeOrgID, databaseID: database.id } },
          body: { sql: statement, statements: [{ sql: statement }] },
          parseAs: "json",
        },
      );
      if (error || !data) throw new Error(errorMessage(error, "Query failed"));
      run.response = data;
    } catch (error) {
      run.error = error instanceof Error ? error.message : "Query failed";
    } finally {
      setQueryRuns((current) => [...current, run]);
      if (apiConnected) {
        void apiClient
          .GET("/v1/organizations/{orgID}/databases/{databaseID}/analytics", {
            params: { path: { orgID: activeOrgID, databaseID: database.id } },
            parseAs: "json",
          })
          .then(({ data }) => {
            if (data) {
              setSeries(data);
              setMetrics(databaseMetricsFromSeries(data));
            }
          })
          .catch(() => undefined);
      }
      setQuerying(false);
    }
  }

  const metricCards = [
    {
      label: "Total queries",
      value: compactNumber(metrics.queries),
      note: metrics.available ? "successful DB executions" : "metrics unavailable",
      icon: Activity,
    },
    {
      label: "Rows read",
      value: compactNumber(metrics.rows_read),
      note: metrics.available ? "rows scanned or returned" : "metrics unavailable",
      icon: Rows3,
    },
    {
      label: "Rows written",
      value: compactNumber(metrics.rows_written),
      note: metrics.available ? "rows changed" : "metrics unavailable",
      icon: Rows3,
    },
    {
      label: "Storage used",
      value: formatBytes(metrics.storage_bytes),
      note: metrics.available ? "current SQLite file size" : "metrics unavailable",
      icon: HardDrive,
    },
    {
      label: "Tables",
      value: compactNumber(metrics.table_count),
      note: metrics.available ? "current user tables" : "metrics unavailable",
      icon: Table2,
    },
  ];

  return (
    <>
      <div className="mb-6 flex items-center justify-between gap-4">
        <Tabs
          className="inline-flex max-w-full"
          listClassName="max-w-full"
          tabs={[
            { label: "Overview", value: "overview" },
            { label: "Query", value: "query" },
            { label: "Settings", value: "settings" },
          ]}
          onValueChange={(value) => setTab(value as "overview" | "query" | "settings")}
          value={tab}
          variant="segmented"
        />
        <a
          className="inline-flex h-8 shrink-0 items-center justify-center gap-2 rounded-md bg-kumo-brand px-3 text-sm text-kumo-inverse hover:bg-kumo-brand/90"
          href={`/databases/${database.id}/explore`}
          rel="noreferrer"
          target="_blank"
        >
          <Table2 className="size-4" />
          Explore data
        </a>
      </div>

      {tab === "overview" ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
            {metricCards.map((metric) => (
              <MetricCard key={metric.label} {...metric} />
            ))}
          </div>

          <div className="mt-6 grid gap-6 xl:grid-cols-2">
            <LayerCard>
              <LayerCard.Secondary>
                <Text as="h2" variant="secondary">
                  Query mix
                </Text>
              </LayerCard.Secondary>
              <LayerCard.Primary className="p-4">
                <DatabaseQueryMixChart series={series} />
              </LayerCard.Primary>
            </LayerCard>
            <LayerCard>
              <LayerCard.Secondary>
                <Text as="h2" variant="secondary">
                  Row activity
                </Text>
              </LayerCard.Secondary>
              <LayerCard.Primary className="p-4">
                <DatabaseRowsChart series={series} />
              </LayerCard.Primary>
            </LayerCard>
            <LayerCard>
              <LayerCard.Secondary>
                <Text as="h2" variant="secondary">
                  Query latency distribution
                </Text>
              </LayerCard.Secondary>
              <LayerCard.Primary className="p-4">
                <DatabaseLatencyHistogram metrics={metrics} />
              </LayerCard.Primary>
            </LayerCard>
            <LayerCard>
              <LayerCard.Secondary>
                <Text as="h2" variant="secondary">
                  Storage and schema
                </Text>
              </LayerCard.Secondary>
              <LayerCard.Primary className="p-4">
                <DatabaseStorageChart series={series} />
              </LayerCard.Primary>
            </LayerCard>
            <LayerCard>
              <LayerCard.Secondary>
                <Text as="h2" variant="secondary">
                  Query latency over time
                </Text>
              </LayerCard.Secondary>
              <LayerCard.Primary className="p-4">
                <DatabaseLatencySeriesChart series={series} />
              </LayerCard.Primary>
            </LayerCard>
          </div>

          <div className="mt-6">
            <LayerCard className="overflow-hidden">
              <div className="border-b border-[#e8e3d9] px-5 py-4">
                <Field label="Bound workers">
                  <Text size="sm" variant="secondary">
                    Workers that can access this database through an active deployment.
                  </Text>
                </Field>
              </div>
              {bindings.length ? (
                <div className="divide-y divide-gray-200">
                  {bindings.map(({ worker, binding }) => (
                    <div
                      key={`${worker.id}-${binding.binding}`}
                      className="flex items-center justify-between gap-4 px-5 py-4"
                    >
                      <div className="min-w-0">
                        <Text bold truncate>
                          {worker.name}
                        </Text>
                        <Text variant="mono-secondary" truncate>
                          {binding.binding}
                        </Text>
                      </div>
                      <Badge variant="success">
                        <Waypoints className="mr-1 inline size-3" />
                        Bound
                      </Badge>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="px-5 py-12">
                  <WorkerDetailEmpty
                    icon={<Database />}
                    title="No worker bindings"
                    copy="Add this database to a worker's db config and deploy it."
                  />
                </div>
              )}
            </LayerCard>
          </div>
        </>
      ) : tab === "query" ? (
        <LayerCard className="overflow-hidden">
          <div className="flex h-[calc(100dvh-164px)] min-h-[520px] flex-col overflow-hidden bg-white">
            <div className="min-h-0 flex-1 bg-[#f8faf9]">
              <div className="h-full overflow-auto">
                <div
                  className={
                    queryRuns.length
                      ? "space-y-4 p-5"
                      : "flex min-h-full items-center justify-center p-5"
                  }
                >
                  {queryRuns.length ? (
                    queryRuns.map((run) => <QueryRunCard key={run.id} run={run} />)
                  ) : (
                    <div className="max-w-sm text-center">
                      <Database className="mx-auto mb-2 size-6" />
                      <Text bold size="sm">
                        No query run yet
                      </Text>
                      <Text size="xs" variant="secondary">
                        Run SQL below to build a console history of statements and results.
                      </Text>
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
              <Button disabled={querying} onClick={() => void runQuery()}>
                <Play className="size-3.5" />
                Run
              </Button>
            </div>
          </div>
        </LayerCard>
      ) : (
        <div className="space-y-6">
          <LayerCard>
            <LayerCard.Secondary>
              <Text as="p" size="xs" variant="secondary">
                Database
              </Text>
              <Text as="h2" DANGEROUS_className="mt-0.5" variant="heading3">
                Basic info
              </Text>
            </LayerCard.Secondary>
            <LayerCard.Primary className="p-4">
              <div className="overflow-hidden rounded-lg border border-[#e2ddd2]">
                {[
                  ["Database ID", database.id],
                  ["Name", database.name],
                  ["Engine", "SQLite"],
                  ["Created", new Date(database.created_at).toLocaleString()],
                  ["Bindings", String(bindings.length)],
                ].map(([label, value]) => (
                  <div
                    key={label}
                    className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]"
                  >
                    <span className="font-mono text-[10px] text-[#93978f]">{label}</span>
                    <span className="break-all font-mono text-[11px] font-bold text-[#4f5a55]">
                      {value}
                    </span>
                  </div>
                ))}
              </div>
            </LayerCard.Primary>
          </LayerCard>
          <LayerCard>
            <LayerCard.Secondary>
              <Text as="h2" variant="heading3">
                Danger zone
              </Text>
            </LayerCard.Secondary>
            <LayerCard.Primary className="p-4">
              <div className="grid gap-4 md:grid-cols-[1fr_auto] md:items-center">
                <div>
                  <Text bold size="sm">
                    Delete database
                  </Text>
                  <Text DANGEROUS_className="mt-1" size="sm" variant="secondary">
                    Permanently remove this database and its stored data. Databases with active
                    worker bindings must be unbound first.
                  </Text>
                </div>
                <Button
                  disabled={bindings.length > 0}
                  onClick={() => {
                    setDeleteError("");
                    setDeleteOpen(true);
                  }}
                  variant="destructive"
                >
                  <Trash2 className="size-4" />
                  Delete database
                </Button>
              </div>
            </LayerCard.Primary>
          </LayerCard>
          <ConfirmDeleteDialog
            confirmLabel="Delete database"
            description="This action cannot be undone. All data stored in this database will be permanently deleted."
            errorMessage={deleteError}
            loading={deleting}
            onConfirm={deleteDatabase}
            onOpenChange={setDeleteOpen}
            open={deleteOpen}
            title="Delete database"
          />
        </div>
      )}
    </>
  );
}

function QueryRunCard({ run }: { run: QueryRun }) {
  const result = run.response?.results?.[0];
  const rows = result?.results ?? [];
  const columns = columnsForRows(rows);
  const status = run.error || resultSummary(result, run.response?.bookmark);

  return (
    <div className="overflow-hidden">
      <pre className="overflow-auto px-4 pt-4 pb-3 font-mono text-sm">
        <code>{"> " + run.sql}</code>
      </pre>
      {run.error ? (
        <div className="px-4 py-4">
          <Text size="sm" variant="error">
            {run.error}
          </Text>
        </div>
      ) : columns.length ? (
        <div className="overflow-auto">
          <Table className="min-w-[720px] border-collapse [&_th]:bg-transparent" layout="fixed">
            <Table.Header>
              <Table.Row>
                {columns.map((column) => (
                  <Table.Head key={column}>
                    <Text variant="mono-secondary" truncate>
                      {column}
                    </Text>
                  </Table.Head>
                ))}
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {rows.map((row, rowIndex) => (
                <Table.Row key={rowIndex}>
                  {columns.map((column) => (
                    <Table.Cell key={column}>
                      <Text title={formatCell(row[column])} truncate variant="mono">
                        {formatCell(row[column])}
                      </Text>
                    </Table.Cell>
                  ))}
                </Table.Row>
              ))}
            </Table.Body>
          </Table>
        </div>
      ) : (
        <div className="px-4 py-5">
          <Text size="sm" variant="secondary">
            Statement complete. No rows returned.
          </Text>
        </div>
      )}
      <div className="border-t border-gray-200 bg-gray-50  py-2 text-xs">
        <Text variant={run.error ? "error" : "mono-secondary"}>{status}</Text>
      </div>
    </div>
  );
}

function MetricCard({
  icon: Icon,
  label,
  note,
  value,
}: {
  icon: LucideIcon;
  label: string;
  note: string;
  value: string;
}) {
  return (
    <LayerCard className="px-5 py-4">
      <div className="flex items-center justify-between gap-3">
        <Text bold size="xs" variant="secondary">
          {label}
        </Text>
        <span className="grid size-7 place-items-center rounded-md bg-kumo-info/15 text-kumo-info">
          <Icon size={14} />
        </span>
      </div>
      <Text as="p" DANGEROUS_className="mt-3" variant="heading3">
        {value}
      </Text>
      <Text size="xs" variant="secondary">
        {note}
      </Text>
    </LayerCard>
  );
}

function DatabaseLatencyHistogram({ metrics }: { metrics: DatabaseMetrics }) {
  const data = latencyHistogramData(metrics);
  const hasSamples = data.some((item) => item.count > 0);
  if (!hasSamples) {
    return (
      <div className="flex min-h-64 items-center justify-center text-center">
        <div>
          <Gauge className="mx-auto mb-2 size-6 text-[#7b827b]" />
          <Text bold size="sm">
            No latency samples yet
          </Text>
          <Text size="xs" variant="secondary">
            Run a query or send database traffic to populate the histogram.
          </Text>
        </div>
      </div>
    );
  }
  return (
    <Chart
      echarts={echarts}
      height={288}
      options={{
        grid: { bottom: 34, left: 44, right: 10, top: 12 },
        tooltip: { trigger: "axis", valueFormatter: (value) => compactNumber(Number(value)) },
        xAxis: {
          axisLine: { show: false },
          axisTick: { show: false },
          data: data.map((item) => item.label),
          type: "category",
        },
        yAxis: {
          axisLine: { show: false },
          axisTick: { show: false },
          axisLabel: { formatter: compactNumber },
          splitLine: { lineStyle: { color: "#e5e7eb" } },
          type: "value",
        },
        series: [
          {
            data: data.map((item) => item.count),
            itemStyle: { color: ChartPalette.categorical(4) },
            lineStyle: { width: 2 },
            name: "Queries",
            showSymbol: false,
            type: "line",
          },
        ],
      }}
    />
  );
}

function DatabaseQueryMixChart({ series }: { series: DatabaseMetricsTimeseries }) {
  return (
    <DatabaseMetricTimeseriesLines
      emptyCopy="Run read or write queries while Prometheus is scraping to populate query series."
      series={[
        { key: "total", label: "Total", points: series.queries ?? [] },
        { key: "read", label: "Read", points: series.read_queries ?? [] },
        { key: "write", label: "Write", points: series.write_queries ?? [] },
      ]}
      valueLabel="Queries"
    />
  );
}

function DatabaseRowsChart({ series }: { series: DatabaseMetricsTimeseries }) {
  return (
    <DatabaseMetricTimeseriesLines
      emptyCopy="Run queries that read or write rows while Prometheus is scraping to populate row series."
      series={[
        { key: "read", label: "Read", points: series.rows_read ?? [] },
        { key: "written", label: "Written", points: series.rows_written ?? [] },
      ]}
      valueLabel="Rows"
    />
  );
}

function DatabaseStorageChart({ series }: { series: DatabaseMetricsTimeseries }) {
  return (
    <DatabaseMetricTimeseriesLines
      emptyCopy="Create tables or write data while Prometheus is scraping to populate storage and schema series."
      series={[
        {
          key: "storage",
          label: "Storage",
          points: series.storage_bytes ?? [],
          formatter: formatBytes,
        },
        { key: "tables", label: "Tables", points: series.table_count ?? [] },
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
        {
          key: "p50",
          label: "P50",
          points: series.p50_latency_ms ?? [],
          formatter: formatQueryDuration,
        },
        {
          key: "p95",
          label: "P95",
          points: series.p95_latency_ms ?? [],
          formatter: formatQueryDuration,
        },
        {
          key: "p99",
          label: "P99",
          points: series.p99_latency_ms ?? [],
          formatter: formatQueryDuration,
        },
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

function DatabaseMetricTimeseriesLines({
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
    <TimeseriesChart
      ariaDescription={`${valueLabel} over time.`}
      data={toKumoTimeseries(series)}
      echarts={echarts}
      height={288}
      tooltipValueFormat={(value) => compactNumber(value)}
      xAxisTickFormat={formatSeriesTickAt}
      yAxisTickFormat={compactNumber}
    />
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
    <TimeseriesChart
      ariaDescription={`${valueLabel} over time.`}
      data={toKumoTimeseries(series)}
      echarts={echarts}
      height={288}
      tooltipValueFormat={formatQueryDuration}
      xAxisTickFormat={formatSeriesTickAt}
      yAxisTickFormat={formatQueryDuration}
    />
  );
}

function DatabaseChartEmpty({ copy }: { copy: string }) {
  return (
    <div className="flex min-h-64 items-center justify-center text-center">
      <div>
        <Gauge className="mx-auto mb-2 size-6 text-[#7b827b]" />
        <Text bold size="sm">
          No chart data yet
        </Text>
        <Text size="xs" variant="secondary">
          {copy}
        </Text>
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

function toKumoTimeseries(series: DatabaseSeries[]) {
  return series.map((entry, index) => ({
    color: ChartPalette.categorical(index),
    data: entry.points.flatMap((point) => {
      const timestamp = new Date(point.timestamp).getTime();
      return Number.isNaN(timestamp) ? [] : [[timestamp, point.value] as [number, number]];
    }),
    name: entry.label,
  }));
}

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
  return [...rows.values()].sort(
    (a, b) =>
      new Date(typeof a.timestamp === "string" ? a.timestamp : "").getTime() -
      new Date(typeof b.timestamp === "string" ? b.timestamp : "").getTime(),
  );
}

function formatSeriesTick(timestamp: string) {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function formatSeriesTickAt(timestamp: number) {
  return formatSeriesTick(new Date(timestamp).toISOString());
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
    rows_returned: [],
    rows_written: [],
    storage_bytes: [],
    table_count: [],
    duration_total_ms: [],
    p50_latency_ms: [],
    p95_latency_ms: [],
    p99_latency_ms: [],
  };
}

function databaseMetricsFromSeries(series: DatabaseMetricsTimeseries): DatabaseMetrics {
  const latest = (points: MetricPoint[] | null | undefined) =>
    points?.[points.length - 1]?.value ?? 0;
  return {
    ...emptyDatabaseMetrics(),
    available: Boolean(series.available),
    queries: latest(series.queries),
    read_queries: latest(series.read_queries),
    write_queries: latest(series.write_queries),
    rows_read: latest(series.rows_read),
    rows_returned: latest(series.rows_returned),
    rows_written: latest(series.rows_written),
    storage_bytes: latest(series.storage_bytes),
    table_count: latest(series.table_count),
    total_duration_ms: latest(series.duration_total_ms),
    p50_duration_ms: latest(series.p50_latency_ms),
    p99_duration_ms: latest(series.p99_latency_ms),
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
  return Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(
    value,
  );
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
    return "[Unserializable value]";
  }
}
