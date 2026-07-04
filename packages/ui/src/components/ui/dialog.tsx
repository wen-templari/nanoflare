import type { ReactNode } from "react";
import { X } from "lucide-react";
import { cn } from "../../lib/utils";
import { Button } from "./button";

export function Dialog({
  open,
  title,
  description,
  children,
  onClose,
  panelClassName,
}: {
  open: boolean;
  title: string;
  description: string;
  children: ReactNode;
  onClose: () => void;
  panelClassName?: string;
}) {
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-[#17211f]/55 p-4 backdrop-blur-sm" onMouseDown={onClose}>
      <section
        className={cn("animate-pop w-full max-w-md rounded-xl border border-white/35 bg-[#f8f5ee] p-5 shadow-2xl", panelClassName)}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <p className=" text-2xl leading-none text-[#25312e]">{title}</p>
            <p className="mt-2 text-xs leading-5 text-[#788078]">{description}</p>
          </div>
          <Button variant="ghost" size="icon" aria-label="Close dialog" onClick={onClose}><X className="size-4" /></Button>
        </div>
        <div className="mt-5">{children}</div>
      </section>
    </div>
  );
}
