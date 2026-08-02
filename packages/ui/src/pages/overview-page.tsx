import { ChartPalette, LayerCard, Text, TimeseriesChart } from "@cloudflare/kumo";
import { ArrowUpRight, DatabaseZap, KeyRound, Waypoints } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import type { WorkerTraffic } from "../app/types";

import { activeOrgID, apiClient } from "../app/api";
import { useAuth } from "../app/auth-context";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { useWorkspace } from "../app/workspace-context";
import { EmptyMetrics, PageHeading, Panel } from "../components/shared/primitives";
import { echarts } from "../lib/kumo-echarts";

export function OverviewPage() {
  const navigate = useNavigate();
  const { userEmail } = useAuth();
  const { workers, apiConnected } = useWorkspace();
  useWorkspaceResources(["workers"]);
  const userName = displayNameFromEmail(userEmail);
  const [traffic, setTraffic] = useState<WorkerTraffic[]>([]);

  useEffect(() => {
    let cancelled = false;

    async function loadTraffic() {
      if (!apiConnected || !workers.length) {
        setTraffic([]);
        return;
      }

      const { data } = await apiClient.GET("/v1/organizations/{orgID}/workers/analytics", {
        params: { path: { orgID: activeOrgID() } },
        parseAs: "json",
      });
      if (!cancelled) setTraffic(data ? [toTraffic(data)] : []);
    }

    void loadTraffic();
    return () => {
      cancelled = true;
    };
  }, [apiConnected, workers.length]);

  const metrics = aggregateTraffic(traffic);
  const stats = [
    {
      label: "Workers",
      value: workers.length,
      note: "Open Workers to inspect deployment status",
      icon: Waypoints,
      href: "/workers",
    },
    {
      label: "KV",
      value: "—",
      note: "Open KV to load namespace inventory",
      icon: KeyRound,
      href: "/kv",
    },
    {
      label: "Object storage",
      value: "—",
      note: "Open Object storage to load bucket inventory",
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
            label="Total requests"
            value={metrics.available ? formatCount(metrics.totalRequests) : "—"}
            note="Cumulative requests across all workers"
            values={metrics.totalRequestSeries}
          />
          <MetricPanel
            label="Worker invocations"
            value={metrics.available ? formatCount(metrics.invocations) : "—"}
            note="Invocations per five-minute interval"
            values={metrics.traffic}
          />
          <MetricPanel
            label="Worker errors"
            value={metrics.available ? formatCount(metrics.errors) : "—"}
            note="5xx responses per five-minute interval"
            values={metrics.errorSeries}
          />
          <MetricPanel
            label="Worker CPU time p90"
            value={metrics.available ? formatMilliseconds(metrics.cpuTimeP90) : "—"}
            note="p90 execution time across reporting workers"
            values={metrics.cpuTimeP90Series}
          />
        </div>
      </section>
    </>
  );
}

function toTraffic(data: {
  available?: boolean;
  total_requests?: { value: number }[] | null;
  invocations?: { value: number }[] | null;
  requests?: { value: number }[] | null;
  errors?: { value: number }[] | null;
  cpu_time_p90_ms?: { value: number }[] | null;
}): WorkerTraffic {
  const requests = data.invocations ?? data.requests ?? [];
  const errors = data.errors ?? [];
  const totalRequests = data.total_requests ?? [];
  const cpuTimeP90 = data.cpu_time_p90_ms ?? [];
  return {
    available: Boolean(data.available),
    requests_per_second: (requests[requests.length - 1]?.value ?? 0) / 300,
    p95_latency: 0,
    error_rate: 0,
    invocations: requests[requests.length - 1]?.value ?? 0,
    errors: errors[errors.length - 1]?.value ?? 0,
    bundle_size: 0,
    total_requests: totalRequests.map((point) => point.value),
    traffic: requests.map((point) => point.value),
    error_series: errors.map((point) => point.value),
    cpu_time_p90_ms: cpuTimeP90.map((point) => point.value),
    duration_ms_avg: 0,
    duration_ms_p95: 0,
    duration_ms_per_second: 0,
    duration_series: [],
    status_codes: [],
  };
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
    <LayerCard>
      <div className="p-4 bg-white ">
        <Text as="p" variant="secondary" size="sm">
          {label}
        </Text>
        <Text as="p" variant="heading2">
          {value}
        </Text>
        <div className="">
          {values.length ? (
            <OverviewTrafficChart label={label} values={values} />
          ) : (
            <EmptyMetrics />
          )}
        </div>
      </div>
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
        timestamp === lastTimestamp
          ? "Now"
          : `${Math.round((lastTimestamp - timestamp) / 3_600_000)}h`
      }
      yAxisTickFormat={formatCount}
    />
  );
}

function aggregateTraffic(items: WorkerTraffic[]) {
  const reporting = items.filter((item) => item.available);
  const invocations = reporting.reduce((sum, item) => sum + item.invocations, 0);
  const errors = reporting.reduce((sum, item) => sum + item.errors, 0);

  return {
    available: reporting.length > 0,
    totalRequests: reporting.reduce((sum, item) => {
      const values = item.total_requests ?? [];
      return sum + (values[values.length - 1] ?? 0);
    }, 0),
    invocations,
    errors,
    traffic: sumSeries(reporting.map((item) => item.traffic ?? [])),
    totalRequestSeries: sumSeries(reporting.map((item) => item.total_requests ?? [])),
    errorSeries: sumSeries(reporting.map((item) => item.error_series ?? [])),
    cpuTimeP90: Math.max(
      0,
      ...reporting.map((item) => {
        const values = item.cpu_time_p90_ms ?? [];
        return values[values.length - 1] ?? 0;
      }),
    ),
    cpuTimeP90Series: sumSeries(reporting.map((item) => item.cpu_time_p90_ms ?? [])),
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
