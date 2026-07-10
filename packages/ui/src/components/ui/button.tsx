import type { ButtonHTMLAttributes } from "react";
import { Button as MantineButton, type ButtonProps as MantineButtonProps } from "@mantine/core";

type LegacyVariant = "default" | "outline" | "ghost" | "dark" | "danger";
type LegacySize = "default" | "sm" | "icon";
export type ButtonProps = Omit<MantineButtonProps, "variant" | "size"> & ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: LegacyVariant;
  size?: LegacySize;
};

const variantMap: Record<LegacyVariant, MantineButtonProps["variant"]> = {
  default: "filled",
  outline: "outline",
  ghost: "subtle",
  dark: "filled",
  danger: "subtle",
};

export function Button({ variant = "default", size = "default", color, children, ...props }: ButtonProps) {
  return (
    <MantineButton
      color={color ?? (variant === "danger" ? "red" : variant === "dark" ? "dark" : undefined)}
      px={size === "icon" ? "xs" : undefined}
      size={size === "sm" || size === "icon" ? "xs" : "sm"}
      variant={variantMap[variant]}
      {...props}
    >
      <span className="inline-flex items-center gap-1.5">{children}</span>
    </MantineButton>
  );
}
