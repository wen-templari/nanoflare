import type { MouseEvent } from "react";

import { Tooltip, TooltipProvider } from "@cloudflare/kumo";

type CopyableResourceIDProps = {
  value: string;
  onCopied: () => void;
};

export function CopyableResourceID({ value, onCopied }: CopyableResourceIDProps) {
  async function copy(event: MouseEvent<HTMLButtonElement>) {
    event.stopPropagation();
    try {
      await navigator.clipboard.writeText(value);
      onCopied();
    } catch {
      // Clipboard access can be unavailable in an insecure browser context.
    }
  }

  return (
    <TooltipProvider>
      <Tooltip
        content="Click to copy"
        render={
          <button
            aria-label="Copy ID"
            className="block max-w-full truncate rounded px-1 font-mono text-kumo-secondary hover:bg-kumo-tint hover:text-kumo-default"
            onClick={(event) => void copy(event)}
            type="button"
          >
            {value}
          </button>
        }
      />
    </TooltipProvider>
  );
}
