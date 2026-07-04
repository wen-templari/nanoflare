import { type Dispatch, type FormEvent, type SetStateAction, useState } from "react";
import { errorText } from "../../app/api";
import type { ObjectStorageBucket } from "../../app/types";
import { sortObjectStorageBuckets } from "../../app/utils";
import { Field } from "../shared/primitives";
import { Button } from "../ui/button";
import { Dialog } from "../ui/dialog";
import { Input } from "../ui/input";

export function CreateObjectStorageBucketDialog({
  open,
  onClose,
  buckets,
  setBuckets,
  notify,
  apiConnected,
}: {
  open: boolean;
  onClose: () => void;
  buckets: ObjectStorageBucket[];
  setBuckets: Dispatch<SetStateAction<ObjectStorageBucket[]>>;
  notify: (text: string) => void;
  apiConnected: boolean;
}) {
  const [name, setName] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return notify("Bucket name is required");
    let bucket: ObjectStorageBucket = { id: crypto.randomUUID().replace(/-/g, ""), name: trimmed, created_at: new Date().toISOString() };
    if (apiConnected) {
      const response = await fetch("/v1/object-storage-buckets", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ name: trimmed }),
      });
      if (!response.ok) return notify(await errorText(response, "Bucket creation failed"));
      bucket = await response.json() as ObjectStorageBucket;
    }
    setBuckets(sortObjectStorageBuckets([...buckets, bucket]));
    setName("");
    onClose();
    notify(`${bucket.name} created`);
  }

  return <Dialog open={open} onClose={onClose} title="Create object storage bucket" description="Provision a shared object bucket you can bind into one or more workers."><form className="space-y-4" onSubmit={submit}><Field label="Name"><Input required placeholder="customer-files" value={name} onChange={(event) => setName(event.target.value)} /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Create bucket</Button></div></form></Dialog>;
}
