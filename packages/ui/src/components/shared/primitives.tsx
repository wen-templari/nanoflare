import type { ReactNode } from "react";

import { Meter, Text } from "@cloudflare/kumo";
import { Activity } from "lucide-react";
export { Field } from "@cloudflare/kumo";

export function PageHeading({
  title,
  copy,
  actions,
}: {
  eyebrow: string;
  title: string;
  copy?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-10 flex items-start justify-between gap-6 py-2">
      <div>
        <Text as="h1" DANGEROUS_className="flex items-center" variant="heading1">
          {title}
        </Text>
        {copy && (
          <Text DANGEROUS_className="mt-2 max-w-[620px]" variant="secondary">
            {copy}
          </Text>
        )}
      </div>
      {actions}
    </div>
  );
}

export function Event({ icon, text, time }: { icon: ReactNode; text: string; time: string }) {
  return (
    <div className="flex flex-nowrap items-center gap-3 py-3">
      <div className="grid size-[34px] place-items-center rounded-lg bg-kumo-info/15 text-kumo-info [&_svg]:size-4">
        {icon}
      </div>
      <Text DANGEROUS_className="flex-1" size="sm">
        {text}
      </Text>
      <Text variant="mono-secondary">{time}</Text>
    </div>
  );
}

export function WorkerDetailEmpty({
  icon,
  title,
  copy,
}: {
  icon: ReactNode;
  title: string;
  copy: string;
}) {
  return (
    <div className="flex min-h-[360px] items-center justify-center">
      <div className="flex flex-col items-center gap-1 text-center [&_svg]:size-6">
        {icon}
        <Text size="sm">{title}</Text>
        <Text size="xs" variant="secondary">
          {copy}
        </Text>
      </div>
    </div>
  );
}

export function EmptyMetrics() {
  return (
    <div className="flex h-[220px] items-center justify-center">
      <div className="flex flex-col items-center gap-1 text-center">
        <Activity size={22} />
        <Text size="sm">No traffic samples yet</Text>
        <Text size="xs" variant="secondary">
          Start the stack or send a request through Traefik
        </Text>
      </div>
    </div>
  );
}

export function StatusCodeMix({ values }: { values: { code: string; value: number }[] }) {
  const total = values.reduce((sum, { value }) => sum + value, 0);
  if (!values.length) return <EmptyMetrics />;

  return (
    <div className="flex flex-col gap-4">
      {values.map(({ code, value }) => (
        <div key={code}>
          <div className="mb-1 flex justify-between gap-3">
            <Text size="xs">HTTP {code}</Text>
            <Text variant="mono-secondary">
              {new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(value)}
            </Text>
          </div>
          <Meter label={`HTTP ${code}`} value={total ? Math.max((value / total) * 100, 2) : 0} />
        </div>
      ))}
    </div>
  );
}
