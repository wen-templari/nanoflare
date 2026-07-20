import { Card, Group, SimpleGrid, Stack, Text, ThemeIcon, Title, UnstyledButton } from "@mantine/core";
import { Archive, ArrowUpRight, CloudUpload, Code2, DatabaseZap, KeyRound, Waypoints } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Bar, BarChart, ResponsiveContainer, Tooltip, XAxis } from "recharts";
import { useAuth } from "../app/auth-context";
import { useWorkspace } from "../app/workspace-context";
import { Event, PageHeading, Panel } from "../components/shared/primitives";

export function OverviewPage() {
  const navigate = useNavigate();
  const { userEmail } = useAuth();
  const { workers, namespaces, objectStorageBuckets } = useWorkspace();
  const userName = displayNameFromEmail(userEmail);
  const kvBindings = workers.reduce((count, worker) => count + (worker.bindings?.filter((binding) => binding.kind === "kv").length ?? 0), 0);
  const objectBindings = workers.reduce((count, worker) => count + (worker.bindings?.filter((binding) => binding.kind === "object_storage_bucket").length ?? 0), 0);
  const stats = [
    { label: "Workers", value: workers.length, note: `${workers.filter((worker) => worker.status === "live").length} live · ${workers.filter((worker) => worker.status === "draft").length} draft`, icon: Waypoints, href: "/workers" },
    { label: "KV", value: namespaces.length, note: `${kvBindings} active bindings across workers`, icon: KeyRound, href: "/kv" },
    { label: "Object storage", value: objectStorageBuckets.length, note: `${objectBindings} active bucket bindings`, icon: DatabaseZap, href: "/object-storage" },
  ];

  return (
    <>
      <PageHeading eyebrow="Sunday, 31 May" title={`Good afternoon, ${userName}.`} copy="Your private runtime is steady. Here is the shape of your workspace today." />
      <SimpleGrid cols={{ base: 1, md: 3 }} spacing="md">
        {stats.map(({ label, value, note, icon: Icon, href }, index) => (
          <UnstyledButton key={label} onClick={() => navigate(href)} style={{ animationDelay: `${index * 80}ms` }}>
            <Card h="100%" padding="lg" radius="lg" withBorder>
              <Group justify="space-between">
                <ThemeIcon variant="light"><Icon size={18} /></ThemeIcon>
                <ArrowUpRight size={16} />
              </Group>
              <Title mt="xl" order={2}>{value}</Title>
              <Text fw={700} mt="xs">{label}</Text>
              <Text c="dimmed" size="xs">{note}</Text>
            </Card>
          </UnstyledButton>
        ))}
      </SimpleGrid>
      <div className="mt-6 grid gap-6 lg:grid-cols-[1.5fr_1fr]">
        <Panel title="Runtime activity" eyebrow="Last 24 hours">
          <RuntimeActivityChart />
        </Panel>
        <Panel title="Recent events" eyebrow="Live log">
          <Stack gap={0}>
            <Event icon={<CloudUpload />} text="worker bundle deployed" time="34m" />
            <Event icon={<KeyRound />} text="env.KV binding refreshed" time="2h" />
            <Event icon={<DatabaseZap />} text="object bucket binding refreshed" time="3h" />
            <Event icon={<Code2 />} text="billing-sync deployed" time="5h" />
            <Event icon={<Archive />} text="previous generation retired" time="8h" />
          </Stack>
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

const runtimeActivity = [35, 44, 37, 58, 65, 52, 76, 68, 88, 72, 82, 96, 77, 64, 73, 56, 61, 49, 66, 72, 60, 52, 44, 59].map((requests, hour) => ({
  hour: hour === 23 ? "NOW" : `${hour}:00`,
  requests,
}));

function RuntimeActivityChart() {
  return (
    <div className="h-64">
      <ResponsiveContainer height="100%" width="100%">
        <BarChart data={runtimeActivity}>
          <XAxis axisLine={false} dataKey="hour" interval={5} tickLine={false} />
          <Tooltip cursor={{ fill: "var(--mantine-color-blue-0)" }} />
          <Bar dataKey="requests" fill="var(--mantine-color-blue-6)" radius={[4, 4, 0, 0]} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
