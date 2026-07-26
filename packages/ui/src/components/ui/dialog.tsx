import type { ReactNode } from "react";
import { Dialog as KumoDialog, Text } from "@cloudflare/kumo";

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
  return (
    <KumoDialog.Root open={open} onOpenChange={(nextOpen) => !nextOpen && onClose()}>
      <KumoDialog className={panelClassName} size={panelClassName?.includes("2xl") ? "lg" : "base"}>
        <div className="flex flex-col gap-4">
          <div className="flex items-start justify-between gap-4">
            <KumoDialog.Title>{title}</KumoDialog.Title>
            <KumoDialog.Close aria-label="Close" />
          </div>
          <Text size="sm" variant="secondary">
            {description}
          </Text>
          {children}
        </div>
      </KumoDialog>
    </KumoDialog.Root>
  );
}
