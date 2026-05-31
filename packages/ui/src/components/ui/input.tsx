import type { InputHTMLAttributes } from "react";
import { cn } from "../../lib/utils";

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={cn(
        "h-10 w-full rounded-md border border-[#d6d0c3] bg-white/80 px-3 text-sm text-[#26332f] outline-none transition placeholder:text-[#a09f98] focus:border-[#e25b3f] focus:ring-2 focus:ring-[#e25b3f]/15",
        className,
      )}
      {...props}
    />
  );
}
