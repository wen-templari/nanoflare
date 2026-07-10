import type { ReactNode } from "react";
import { Modal, Stack, Text } from "@mantine/core";

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
    <Modal opened={open} onClose={onClose} title={title} centered size={panelClassName?.includes("2xl") ? "lg" : "md"}>
      <Stack gap="md">
        <Text c="dimmed" size="sm">{description}</Text>
        {children}
      </Stack>
    </Modal>
  );
}
