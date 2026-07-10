import { Badge as MantineBadge, type BadgeProps as MantineBadgeProps } from "@mantine/core";

export function Badge({
  tone = "neutral",
  children,
  ...props
}: MantineBadgeProps & { tone?: "neutral" | "green" | "orange" | "blue" }) {
  const colors = {
    neutral: "gray",
    green: "green",
    orange: "orange",
    blue: "blue",
  };

  return <MantineBadge color={colors[tone]} variant="light" {...props}>{children}</MantineBadge>;
}
