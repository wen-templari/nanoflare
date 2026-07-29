import { Button as KumoButton } from "@cloudflare/kumo";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/utils";

type LegacyVariant = "default" | "outline" | "ghost" | "dark" | "danger";
type LegacySize = "default" | "sm" | "icon";
export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: LegacyVariant;
  size?: LegacySize;
  color?: string;
  loading?: boolean;
  leftSection?: ReactNode;
  rightSection?: ReactNode;
};

export function Button({
  variant = "default",
  size = "default",
  color,
  children,
  className,
  leftSection,
  rightSection,
  ...props
}: ButtonProps) {
  const ButtonBase = KumoButton as any;
  return (
    <ButtonBase
      {...props}
      aria-label={props["aria-label"] ?? (typeof children === "string" ? children : "Action")}
      className={cn(size === "icon" && "size-9 p-0", size === "sm" && "h-6.5 text-xs", className)}
      {...(size === "icon" ? { shape: "square" } : {})}
      variant={
        variant === "danger"
          ? "destructive"
          : variant === "outline"
            ? "outline"
            : variant === "ghost"
              ? "ghost"
              : variant === "default"
                ? "primary"
                : "secondary"
      }
    >
      {leftSection}
      {children}
      {rightSection}
    </ButtonBase>
  );
}
