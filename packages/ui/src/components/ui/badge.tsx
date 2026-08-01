import type { ComponentProps } from "react";

import { Badge as KumoBadge } from "@cloudflare/kumo";

import { cn } from "../../lib/utils";

export function Badge({
  tone = "neutral",
  children,
  className,
  ...props
}: ComponentProps<typeof KumoBadge> & { tone?: "neutral" | "green" | "orange" | "blue" }) {
  return (
    <KumoBadge
      {...props}
      className={cn(
        tone === "green" && "bg-kumo-success/15 text-kumo-success",
        tone === "orange" && "bg-kumo-warning/15 text-kumo-warning",
        tone === "blue" && "bg-kumo-info/15 text-kumo-info",
        className,
      )}
    >
      {children}
    </KumoBadge>
  );
}
