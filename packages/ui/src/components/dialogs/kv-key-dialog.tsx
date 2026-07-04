import { type FormEvent } from "react";
import { Save } from "lucide-react";
import { Field } from "../shared/primitives";
import { Button } from "../ui/button";
import { Dialog } from "../ui/dialog";
import { Input } from "../ui/input";

export function KVKeyDialog({
  open,
  mode,
  keyName,
  value,
  loading,
  submitting,
  onClose,
  onKeyNameChange,
  onValueChange,
  onSubmit,
}: {
  open: boolean;
  mode: "create" | "edit";
  keyName: string;
  value: string;
  loading: boolean;
  submitting: boolean;
  onClose: () => void;
  onKeyNameChange: (nextValue: string) => void;
  onValueChange: (nextValue: string) => void;
  onSubmit: (event: FormEvent) => void;
}) {
  const editing = mode === "edit";

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={editing ? "Edit KV entry" : "Create KV entry"}
      description={editing ? "Update the key name or value for this namespace entry." : "Add a new key and seed its initial value for this namespace."}
      panelClassName="max-w-2xl"
    >
      <form className="space-y-4" onSubmit={onSubmit}>
        <Field label="Key">
          <Input required placeholder="visits" value={keyName} onChange={(event) => onKeyNameChange(event.target.value)} disabled={loading || submitting} />
        </Field>
        <Field label="Value">
          <textarea
            required
            value={value}
            onChange={(event) => onValueChange(event.target.value)}
            disabled={loading || submitting}
            spellCheck={false}
            className="min-h-80 w-full rounded-md border border-[#d6d0c3] bg-[#1f2927] p-4 font-mono text-[11px] leading-6 text-[#d8dfd8] outline-none disabled:cursor-not-allowed disabled:opacity-70"
            placeholder='{"count": 0}'
          />
        </Field>
        <div className="flex items-center justify-between gap-3 rounded-lg border border-[#e2ddd2] bg-white/45 px-4 py-3">
          <p className="font-mono text-[10px]   text-[#7a817a]">{loading ? "Loading key value..." : `${value.length} bytes ready`}</p>
          <div className="flex gap-2">
            <Button type="button" variant="ghost" onClick={onClose} disabled={submitting}>Cancel</Button>
            <Button type="submit" disabled={loading || submitting || !keyName.trim()}><Save className="size-3.5" />{editing ? "Save changes" : "Create key"}</Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
