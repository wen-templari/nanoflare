import { Banner, Button, Dialog, Text } from "@cloudflare/kumo";
import { X } from "lucide-react";

type ConfirmDeleteDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel: string;
  onConfirm: () => void | Promise<void>;
  loading?: boolean;
  errorMessage?: string;
};

export function ConfirmDeleteDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  onConfirm,
  loading = false,
  errorMessage,
}: ConfirmDeleteDialogProps) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog className="p-0">
        <div className="flex items-center justify-between border-b border-kumo-line px-6 py-4">
          <Dialog.Title className="text-lg font-semibold">{title}</Dialog.Title>
          <Dialog.Close
            render={(props) => (
              <Button
                {...props}
                aria-label="Close"
                disabled={loading}
                shape="square"
                size="sm"
                variant="ghost"
              >
                <X className="size-4" />
              </Button>
            )}
          />
        </div>
        <div className="grid gap-4 px-6 py-5">
          {errorMessage && <Banner variant="error">{errorMessage}</Banner>}
          <Text size="sm" variant="secondary">
            {description}
          </Text>
        </div>
        <div className="flex justify-end gap-2 border-t border-kumo-line px-6 py-4">
          <Button
            disabled={loading}
            onClick={() => onOpenChange(false)}
            type="button"
            variant="secondary"
          >
            Cancel
          </Button>
          <Button
            loading={loading}
            onClick={() => void onConfirm()}
            type="button"
            variant="destructive"
          >
            {confirmLabel}
          </Button>
        </div>
      </Dialog>
    </Dialog.Root>
  );
}
