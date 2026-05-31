import type { ButtonHTMLAttributes } from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-[13px] font-semibold transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[#e25b3f]/50 disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        default: "bg-[#e25b3f] text-white shadow-[0_1px_0_#a83a27] hover:bg-[#c94c34]",
        outline: "border border-[#d6d0c3] bg-white/70 text-[#26332f] hover:border-[#9f9a8e] hover:bg-white",
        ghost: "text-[#58615c] hover:bg-[#ebe8de] hover:text-[#26332f]",
        dark: "bg-[#22312d] text-[#f6f2e9] shadow-[0_1px_0_#111a18] hover:bg-[#31433e]",
        danger: "text-[#bd422f] hover:bg-[#f9e1da]",
      },
      size: {
        default: "h-9 px-3.5",
        sm: "h-8 px-3 text-xs",
        icon: "size-9",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {}

export function Button({ className, variant, size, ...props }: ButtonProps) {
  return <button className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
