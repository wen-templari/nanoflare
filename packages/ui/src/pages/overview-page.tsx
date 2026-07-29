import { ChartPalette, LayerCard, Text, TimeseriesChart } from "@cloudflare/kumo";
import {
  ArrowUpRight,
  DatabaseZap,
  KeyRound,
  Waypoints,
} from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { fetchJSON } from "../app/api";
import { useAuth } from "../app/auth-context";
import { useWorkspace } from "../app/workspace-context";
import type { WorkerTraffic } from "../app/types";
import { EmptyMetrics, PageHeading, Panel } from "../components/shared/primitives";
import { echarts } from "../lib/kumo-echarts";

export function OverviewPage() {
  const navigate = useNavigate();
  const { userEmail } = useAuth();
  const { workers, namespaces, objectStorageBuckets, apiConnected } = useWorkspace();
  const userName = displayNameFromEmail(userEmail);
  const [traffic, setTraffic] = useState<WorkerTraffic[]>([]);

  useEffect(() => {
    let cancelled = false;

    async function loadTraffic() {
      if (!apiConnected || !workers.length) {
        setTraffic([]);
        return;
      }

      const results = await Promise.all(
        workers.map((worker) =>
          fetchJSON<WorkerTraffic>(`/v1/workers/${encodeURIComponent(worker.id)}/traffic`).catch(
            () => undefined,
          ),
        ),
      );
      if (!cancelled) setTraffic(results.filter((item): item is WorkerTraffic => Boolean(item)));
    }

    void loadTraffic();
    return () => {
      cancelled = true;
    };
  }, [apiConnected, workers]);

  const metrics = aggregateTraffic(traffic);
  const kvBindings = workers.reduce(
    (count, worker) =>
      count + (worker.bindings?.filter((binding) => binding.kind === "kv").length ?? 0),
    0,
  );
  const objectBindings = workers.reduce(
    (count, worker) =>
      count +
      (worker.bindings?.filter((binding) => binding.kind === "object_storage_bucket").length ?? 0),
    0,
  );
  const stats = [
    {
      label: "Workers",
      value: workers.length,
      note: `${workers.filter((worker) => worker.status === "live").length} live · ${workers.filter((worker) => worker.status === "draft").length} draft`,
      icon: Waypoints,
      href: "/workers",
    },
    {
      label: "KV",
      value: namespaces.length,
      note: `${kvBindings} active bindings across workers`,
      icon: KeyRound,
      href: "/kv",
    },
    {
      label: "Object storage",
      value: objectStorageBuckets.length,
      note: `${objectBindings} active bucket bindings`,
      icon: DatabaseZap,
      href: "/object-storage",
    },
  ];

  return (
    <>
      <PageHeading
        eyebrow="Sunday, 31 May"
        title={`Good afternoon, ${userName}.`}
        copy="A live view of your workspace and the worker traffic collected over the last 24 hours."
      />
      <div className="grid gap-4 md:grid-cols-3">
        {stats.map(({ label, value, note, icon: Icon, href }, index) => (
          <button
            className="text-left"
            key={label}
            onClick={() => navigate(href)}
            style={{ animationDelay: `${index * 80}ms` }}
            type="button"
          >
            <LayerCard className="h-full px-5 py-4">
              <div className="flex items-center justify-between gap-3">
                <span className="grid size-9 place-items-center rounded-lg bg-kumo-info/15 text-kumo-info">
                  <Icon size={18} />
                </span>
                <ArrowUpRight size={16} />
              </div>
              <Text as="p" DANGEROUS_className="mt-6" variant="heading2">
                {value}
              </Text>
              <Text as="p" bold size="sm">
                {label}
              </Text>
              <Text as="p" size="xs" variant="secondary">
                {note}
              </Text>
            </LayerCard>
          </button>
        ))}
      </div>
      <section className="mt-8">
        <div className="mb-4 flex items-end justify-between gap-4">
          <div>
            <Text as="h2" variant="heading2">
              Analytics
            </Text>
            <Text DANGEROUS_className="mt-1" size="sm" variant="secondary">
              {metrics.available
                ? "All deployed workers · last 24 hours"
                : "Connect Prometheus and send worker traffic to populate metrics."}
            </Text>
          </div>
          <Text variant="mono-secondary">{metrics.available ? "LIVE" : "UNAVAILABLE"}</Text>
        </div>
        <div className="grid gap-4 xl:grid-cols-2">
          <MetricPanel
            label="Worker invocations"
            value={metrics.available ? formatCount(metrics.invocations) : "—"}
            note="Requests served across all workers"
            values={metrics.traffic}
          />
          <MetricPanel
            label="Worker request rate"
            value={metrics.available ? `${metrics.requestsPerSecond.toFixed(2)} req/s` : "—"}
            note="Current five-minute average"
            values={metrics.traffic}
          />
        </div>
        <div className="mt-4 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <MetricCard
            label="Worker errors"
            note="5xx responses in 24 hours"
            value={metrics.available ? formatCount(metrics.errors) : "—"}
          />
          <MetricCard
            label="Error rate"
            note="5xx responses / invocations"
            value={metrics.available ? `${(metrics.errorRate * 100).toFixed(2)}%` : "—"}
          />
          <MetricCard
            label="Handler average"
            note="Weighted across workers"
            value={metrics.available ? formatMilliseconds(metrics.durationAverage) : "—"}
          />
          <MetricCard
            label="Highest handler p95"
            note="Across reporting workers"
            value={metrics.available ? formatMilliseconds(metrics.durationP95) : "—"}
          />
        </div>
      </section>
    </>
  );
}

function displayNameFromEmail(email: string) {
  const localPart = email.split("@", 1)[0].trim();
  const firstName = localPart.split(/[._+-]/, 1)[0];

  return firstName ? firstName.charAt(0).toUpperCase() + firstName.slice(1) : "there";
}

function MetricPanel({
  label,
  value,
  note,
  values,
}: {
  label: string;
  value: string;
  note: string;
  values: number[];
}) {
  return (
    <Panel title={label} eyebrow={note}>
      <Text as="p" variant="heading1">
        {value}
      </Text>
      <div className="mt-3">
        {values.length ? <OverviewTrafficChart label={label} values={values} /> : <EmptyMetrics />}
      </div>
    </Panel>
  );
}

function MetricCard({ label, value, note }: { label: string; value: string; note: string }) {
  return (
    <LayerCard className="px-5 py-4">
      <Text as="p" size="sm" variant="secondary">
        {label}
      </Text>
      <Text as="p" DANGEROUS_className="mt-1" variant="heading2">
        {value}
      </Text>
      <Text as="p" DANGEROUS_className="mt-2" size="xs" variant="secondary">
        {note}
      </Text>
    </LayerCard>
  );
}

function OverviewTrafficChart({ label, values }: { label: string; values: number[] }) {
  const now = Date.now();
  const data = values.map(
    (value, index) => [now - (values.length - 1 - index) * 5 * 60_000, value] as [number, number],
  );
  const lastTimestamp = data[data.length - 1][0];

  return (
    <TimeseriesChart
      ariaDescription={`${label} over the last 24 hours.`}
      data={[{ color: ChartPalette.categorical(0), data, name: label }]}
      echarts={echarts}
      gradient
      height={192}
      tooltipValueFormat={(value) => value.toFixed(1)}
      xAxisTickFormat={(timestamp) =>
        timestamp === lastTimestamp ? "Now" : `${Math.round((lastTimestamp - timestamp) / 3_600_000)}h`
      }
      yAxisTickFormat={formatCount}
    />
  );
}

function aggregateTraffic(items: WorkerTraffic[]) {
  const reporting = items.filter((item) => item.available);
  const invocations = reporting.reduce((sum, item) => sum + item.invocations, 0);
  const errors = reporting.reduce((sum, item) => sum + item.errors, 0);
  const durationWeight = reporting.reduce((sum, item) => sum + item.invocations, 0);

  return {
    available: reporting.length > 0,
    invocations,
    errors,
    errorRate: invocations ? errors / invocations : 0,
    requestsPerSecond: reporting.reduce((sum, item) => sum + item.requests_per_second, 0),
    durationAverage: durationWeight
      ? reporting.reduce((sum, item) => sum + item.duration_ms_avg * item.invocations, 0) / durationWeight
      : 0,
    durationP95: reporting.reduce((maximum, item) => Math.max(maximum, item.duration_ms_p95), 0),
    traffic: sumSeries(reporting.map((item) => item.traffic)),
  };
}

function sumSeries(series: number[][]) {
  const length = Math.max(0, ...series.map((values) => values.length));
  return Array.from({ length }, (_, index) =>
    series.reduce((sum, values) => sum + (values[index - (length - values.length)] ?? 0), 0),
  );
}

function formatCount(value: number) {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(
    value || 0,
  );
}

function formatMilliseconds(value: number) {
  if (value >= 1_000) return `${(value / 1_000).toFixed(2)} s`;
  return `${value.toFixed(value < 10 ? 1 : 0)} ms`;
}
