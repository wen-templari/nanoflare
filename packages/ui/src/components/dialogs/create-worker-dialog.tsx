import { type FormEvent, useState } from "react";
import { apiFetch } from "../../app/api";
import type { Worker } from "../../app/types";
import { Dialog } from "../ui/dialog";
import { Input } from "../ui/input";
import { Button } from "../ui/button";
import { Field } from "../shared/primitives";

export function CreateWorkerDialog({
  open,
  onClose,
  workers,
  setWorkers,
  notify,
  apiConnected,
}: {
  open: boolean;
  onClose: () => void;
  workers: Worker[];
  setWorkers: (workers: Worker[]) => void;
  notify: (text: string) => void;
  apiConnected: boolean;
}) {
  const [hostname, setHostname] = useState("");
  const [name, setName] = useState("");
  const [protectedRoutes, setProtectedRoutes] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const auth = { protected_routes: protectedRoutes.split("\n").map((route) => route.trim()).filter(Boolean) };
    let worker: Worker = { id: crypto.randomUUID().replace(/-/g, ""), name, hostname, auth, created_at: new Date().toISOString(), status: "draft", requests: "0", deployment: "awaiting deploy" };
    if (apiConnected) {
      const trimmedHostname = hostname.trim();
      const response = await apiFetch("/v1/workers", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(trimmedHostname ? { name, hostname: trimmedHostname, auth } : { name, auth }) });
      if (!response.ok) return notify("Worker registration failed");
      worker = { ...worker, ...await response.json() as Worker };
    }
    setWorkers([...workers, worker]);
    setName("");
    setHostname("");
    setProtectedRoutes("");
    onClose();
    notify(`${worker.name} registered`);
  }

  return <Dialog open={open} onClose={onClose} title="Register worker" description="Create an isolated runtime target. You can deploy a worker bundle after registration."><form className="space-y-4" onSubmit={submit}><Field label="Name"><Input required placeholder="Analytics worker" value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label="Hostname"><Input placeholder="analytics.acme.internal" value={hostname} onChange={(event) => setHostname(event.target.value)} /></Field><Field label="Protected routes"><textarea value={protectedRoutes} onChange={(event) => setProtectedRoutes(event.target.value)} spellCheck={false} className="min-h-28 w-full rounded-md border border-[#d6d0c3] bg-[#fdfbf6] p-3 font-mono text-[11px] leading-6 text-[#35413e] outline-none" placeholder="/admin/*&#10;/api/private/*" /></Field><div className="flex justify-end gap-2 pt-2"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit">Register worker</Button></div></form></Dialog>;
}
