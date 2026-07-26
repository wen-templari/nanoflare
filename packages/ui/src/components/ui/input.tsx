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
  return <InputBase {...props} className={cn("w-full", inputClassName, className)} />;
}
