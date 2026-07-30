import { Input as KumoInput } from "@cloudflare/kumo";
import type { ChangeEventHandler } from "react";
import { cn } from "../../lib/utils";

type InputProps = {
  [key: string]: any;
  inputClassName?: string;
  className?: string;
  onChange?: ChangeEventHandler<HTMLInputElement>;
};
export function Input({ className, inputClassName, ...props }: InputProps) {
  const InputBase = KumoInput as any;
  const unstyled = props.variant === "unstyled";
  return (
    <InputBase
      {...props}
      {...(unstyled ? { variant: undefined } : {})}
      className={cn(
        "w-full focus:!ring-1.5",
        inputClassName,
        className,
        unstyled && "!border-0 !bg-transparent !shadow-none !ring-0 focus:!ring-0",
      )}
    />
  );
}
