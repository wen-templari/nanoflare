import type { HTMLAttributes } from "react";
import { cn } from "../../lib/utils";

export function Badge({
  className,
  tone = "neutral",
  ...props
}: HTMLAttributes<HTMLSpanElement> & { tone?: "neutral" | "green" | "orange" | "blue" }) {
  const tones = {
    neutral: "border-[#d8d3c7] bg-[#efede6] text-[#666b65]",
    green: "border-[#bfd4c4] bg-[#e1eee3] text-[#397046]",
    orange: "border-[#ecc3b6] bg-[#fae5df] text-[#b14b37]",
    blue: "border-[#bfcfd2] bg-[#e1ecee] text-[#477179]",
  };
  return (
    <span
      className={cn("inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-bold uppercase tracking-[0.12em]", tones[tone], className)}
      {...props}
    />
  );
}
