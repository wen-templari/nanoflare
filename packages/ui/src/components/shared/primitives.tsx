import type { ReactNode } from "react";
import { Activity, MoreHorizontal } from "lucide-react";
import { Box, Card, Center, Group, Input, Progress, Stack, Text, Title } from "@mantine/core";

export function PageHeading({ title, copy, actions }: { eyebrow: string; title: string; copy: string; actions?: ReactNode }) {
  return (
    <Group align="start" justify="space-between" mb={40} py="sm">
      <Box>
        <Title className="flex h-12 items-center" order={1}>{title}</Title>
        <Text c="dimmed" maw={620} mt="sm" size="sm">{copy}</Text>
      </Box>
      {actions}
    </Group>
  );
}

export function Panel({ title, eyebrow, children, flush = false }: { title?: string; eyebrow?: string; children: ReactNode; flush?: boolean }) {
  const hasHeader = Boolean(title || eyebrow);

  return (
    <Card withBorder padding={flush ? 0 : "lg"} radius="lg">
      {hasHeader && (
        <Card.Section withBorder inheritPadding py="md">
          <Group justify="space-between">
            <Box>
              {eyebrow && <Text c="dimmed" fw={700} size="xs" tt="uppercase">{eyebrow}</Text>}
              {title && <Title mt={2} order={3} size="h5">{title}</Title>}
            </Box>
            <MoreHorizontal size={16} />
          </Group>
        </Card.Section>
      )}
      {hasHeader && !flush ? <Box mt="md">{children}</Box> : children}
    </Card>
  );
}

export function Event({ icon, text, time }: { icon: ReactNode; text: string; time: string }) {
  return (
    <Group py="sm" wrap="nowrap">
      <Center bg="blue.0" c="blue" h={34} w={34} className="[&_svg]:size-4">{icon}</Center>
      <Text flex={1} fw={700} size="sm">{text}</Text>
      <Text c="dimmed" ff="monospace" size="xs">{time}</Text>
    </Group>
  );
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return <Input.Wrapper label={label}>{children}</Input.Wrapper>;
}

export function WorkerDetailEmpty({ icon, title, copy }: { icon: ReactNode; title: string; copy: string }) {
  return <Center mih={360}><Stack align="center" gap={4} ta="center" className="[&_svg]:size-6">{icon}<Text fw={700} size="sm">{title}</Text><Text c="dimmed" size="xs">{copy}</Text></Stack></Center>;
}

export function EmptyMetrics() {
  return <Center h={220}><Stack align="center" gap={4} ta="center"><Activity size={22} /><Text fw={700} size="sm">No traffic samples yet</Text><Text c="dimmed" size="xs">Start the stack or send a request through Traefik</Text></Stack></Center>;
}

export function StatusCodeMix({ values }: { values: { code: string; value: number }[] }) {
  const total = values.reduce((sum, { value }) => sum + value, 0);
  if (!values.length) return <EmptyMetrics />;

  return (
    <Stack gap="md">
      {values.map(({ code, value }) => (
        <Box key={code}>
          <Group justify="space-between" mb={4}>
            <Text fw={700} size="xs">HTTP {code}</Text>
            <Text c="dimmed" ff="monospace" size="xs">{value.toFixed(2)}/s</Text>
          </Group>
          <Progress color={code.startsWith("5") ? "red" : code.startsWith("4") ? "orange" : "green"} value={total ? Math.max((value / total) * 100, 2) : 0} />
        </Box>
      ))}
    </Stack>
  );
}
