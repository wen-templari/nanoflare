import { Chart, ChartPalette, LayerCard, Text } from "@cloudflare/kumo";
import {
  Archive,
  ArrowUpRight,
  CloudUpload,
  Code2,
  DatabaseZap,
  KeyRound,
  Waypoints,
} from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../app/auth-context";
import { useWorkspace } from "../app/workspace-context";
import { Event, PageHeading, Panel } from "../components/shared/primitives";
import { echarts } from "../lib/kumo-echarts";

export function OverviewPage() {
  const navigate = useNavigate();
  const { userEmail } = useAuth();
  const { workers, namespaces, objectStorageBuckets } = useWorkspace();
  const userName = displayNameFromEmail(userEmail);
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
        copy="Your private runtime is steady. Here is the shape of your workspace today."
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
      <div className="mt-6 grid gap-6 lg:grid-cols-[1.5fr_1fr]">
        <Panel title="Runtime activity" eyebrow="Last 24 hours">
          <RuntimeActivityChart />
        </Panel>
        <Panel title="Recent events" eyebrow="Live log">
          <div>
            <Event icon={<CloudUpload />} text="worker bundle deployed" time="34m" />
            <Event icon={<KeyRound />} text="env.KV binding refreshed" time="2h" />
            <Event icon={<DatabaseZap />} text="object bucket binding refreshed" time="3h" />
            <Event icon={<Code2 />} text="billing-sync deployed" time="5h" />
            <Event icon={<Archive />} text="previous generation retired" time="8h" />
          </div>
        </Panel>
      </div>
    </>
  );
}

function displayNameFromEmail(email: string) {
  const localPart = email.split("@", 1)[0].trim();
  const firstName = localPart.split(/[._+-]/, 1)[0];

  return firstName ? firstName.charAt(0).toUpperCase() + firstName.slice(1) : "there";
}

const runtimeActivity = [
  35, 44, 37, 58, 65, 52, 76, 68, 88, 72, 82, 96, 77, 64, 73, 56, 61, 49, 66, 72, 60, 52, 44, 59,
].map((requests, hour) => ({
  hour: hour === 23 ? "NOW" : `${hour}:00`,
  requests,
}));

function RuntimeActivityChart() {
  return (
    <Chart
      echarts={echarts}
      height={256}
      options={{
        grid: { bottom: 28, left: 12, right: 8, top: 12 },
        tooltip: { trigger: "axis" },
        xAxis: {
          axisLabel: { interval: 5 },
          axisLine: { show: false },
          axisTick: { show: false },
          data: runtimeActivity.map((item) => item.hour),
          type: "category",
        },
        yAxis: { show: false, type: "value" },
        series: [
          {
            barMaxWidth: 22,
            data: runtimeActivity.map((item) => item.requests),
            itemStyle: { borderRadius: [4, 4, 0, 0], color: ChartPalette.categorical(0) },
            type: "bar",
          },
        ],
      }}
    />
  );
}
