import { type Dispatch, type FormEvent, type SetStateAction, useState } from "react";
import { errorText } from "../../app/api";
import type { KVNamespace } from "../../app/types";
import { sortNamespaces } from "../../app/utils";
import { Dialog } from "../ui/dialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Field } from "../shared/primitives";

export function CreateKVNamespaceDialog({
  open,
  onClose,
  namespaces,
  setNamespaces,
  notify,
  apiConnected,
}: {
  open: boolean;
  onClose: () => void;
  namespaces: KVNamespace[];
  setNamespaces: Dispatch<SetStateAction<KVNamespace[]>>;
  notify: (text: string) => void;
  apiConnected: boolean;
}) {
  const [name, setName] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return notify("Namespace name is required");
    let namespace: KVNamespace = { id: crypto.randomUUID().replace(/-/g, ""), name: trimmed, created_at: new Date().toISOString() };
    if (apiConnected) {
      const response = await fetch("/v1/kv/namespaces", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ name: trimmed }),
      });
      if (!response.ok) return notify(await errorText(response, "Namespace creation failed"));
      namespace = await response.json() as KVNamespace;
    }
    setNamespaces(sortNamespaces([...namespaces, namespace]));
    setName("");
    onClose();
    notify(`${namespace.name} created`);
  }

  return <Dialog open={open} onClose={onClose} title="Create KV namespace" description="Provision a shared namespace you can bind into one or more workers."><form className="space-y-4" onSubmit={submit}><Field label="Name"><Input required placeholder="shared-cache" value={name} onChange={(event) => setName(event.target.value)} /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Create namespace</Button></div></form></Dialog>;
}
