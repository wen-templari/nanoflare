import type { ReactNode } from "react";

import { Button, Dialog as KumoDialog, Text, cn } from "@cloudflare/kumo";
import { X } from "lucide-react";

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
      <KumoDialog
        className={cn("p-6", panelClassName)}
        size={panelClassName?.includes("2xl") ? "lg" : "base"}
      >
        <div className="flex flex-col gap-4">
          <div className="flex items-start justify-between gap-4">
            <KumoDialog.Title className="text-lg font-semibold">{title}</KumoDialog.Title>
            <KumoDialog.Close
              render={(props) => (
                <Button {...props} aria-label="Close" shape="square" size="sm" variant="ghost">
                  <X className="size-4" />
                </Button>
              )}
            />
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
