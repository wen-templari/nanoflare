import { useEffect, useState } from "react";
import { KeyRound, Plus, RefreshCw, Save, Search, Trash2 } from "lucide-react";
import { apiFetch, fetchJSON } from "../../app/api";
import type { KVNamespaceOption, WorkerKVKey } from "../../app/types";
import { formatBytes } from "../../app/utils";
import { Input } from "../ui/input";
import { Button } from "../ui/button";
import { cn } from "../../lib/utils";

export function NamespaceKeyEditor({
  workerID,
  namespaces,
  namespaceID,
  onNamespaceChange,
  notify,
  namespaceDisabled = false,
}: {
  workerID: string;
  namespaces: KVNamespaceOption[];
  namespaceID: string;
  onNamespaceChange: (namespaceID: string) => void;
  notify: (text: string) => void;
  namespaceDisabled?: boolean;
}) {
  const [keys, setKeys] = useState<WorkerKVKey[]>([]);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const namespaceBase = namespaceID ? `/v1/apps/${workerID}/kv/namespaces/${encodeURIComponent(namespaceID)}` : "";
  const path = key.trim() && namespaceBase ? `${namespaceBase}/${encodeURIComponent(key.trim())}` : "";

  useEffect(() => {
    setKeys([]);
    setKey("");
    setValue("");
    setStatus("");
  }, [workerID, namespaceID]);

  async function refreshKeys() {
    if (!namespaceBase) {
      setKeys([]);
      setStatus(namespaces.length ? "Select a KV namespace" : "No KV namespaces bound");
      return;
    }
    setLoading(true);
    setStatus("");
    try {
      setKeys(await fetchJSON<WorkerKVKey[]>(namespaceBase));
      setStatus("Keys refreshed");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV list failed");
    } finally {
      setLoading(false);
    }
  }

  async function readKey(nextKey = key.trim()) {
    if (!nextKey || !namespaceBase) return;
    setLoading(true);
    setStatus("");
    try {
      setKey(nextKey);
      const response = await apiFetch(`${namespaceBase}/${encodeURIComponent(nextKey)}`);
      if (response.status === 404) {
        setValue("");
        setStatus("Key not found");
        return;
      }
      if (!response.ok) throw new Error(`KV read failed (${response.status})`);
      setValue(await response.text());
      setStatus("Value loaded");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV read failed");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refreshKeys();
  }, [workerID, namespaceBase]);

  async function writeKey() {
    if (!path) return;
    setLoading(true);
    setStatus("");
    try {
      const response = await apiFetch(path, { method: "PUT", body: value });
      if (!response.ok) throw new Error(`KV write failed (${response.status})`);
      setStatus("Value saved");
      setKeys(await fetchJSON<WorkerKVKey[]>(namespaceBase));
      notify(`${key.trim()} saved`);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV write failed");
    } finally {
      setLoading(false);
    }
  }

  async function deleteKey() {
    if (!path) return;
    setLoading(true);
    setStatus("");
    try {
      const response = await apiFetch(path, { method: "DELETE" });
      if (!response.ok) throw new Error(`KV delete failed (${response.status})`);
      setValue("");
      setStatus("Key deleted");
      setKeys(await fetchJSON<WorkerKVKey[]>(namespaceBase));
      notify(`${key.trim()} deleted`);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "KV delete failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="grid min-h-[510px] gap-0 md:grid-cols-[260px_1fr]">
      <aside className="border-b border-gray-200 bg-gray-50 py-3 md:border-b-0 md:border-r">
        <div className="flex items-center justify-between px-4 pb-2">
          <p className="font-mono text-[9px]   text-[#a0a39c]">KV keys</p>
          <Button type="button" variant="ghost" size="icon" aria-label="Refresh KV keys" onClick={() => void refreshKeys()} disabled={loading}><RefreshCw className={cn("size-3.5", loading && "animate-spin")} /></Button>
        </div>
        <div className="px-4 pb-3">
          <select value={namespaceID} onChange={(event) => onNamespaceChange(event.target.value)} className="w-full rounded-md border border-[#d6d0c3] bg-white/75 px-3 py-2 font-mono text-[10px] text-[#4c5853] outline-none" disabled={namespaceDisabled || !namespaces.length}>
            {!namespaces.length && <option value="">No KV namespaces</option>}
            {namespaces.map((namespace) => <option key={namespace.id} value={namespace.id}>{namespace.label}</option>)}
          </select>
        </div>
        <button onClick={() => { setKey(""); setValue(""); setStatus("Ready for a new key"); }} className="flex w-full items-center gap-2 px-4 py-2 text-left font-mono text-[10px] font-bold text-[#68716c] transition hover:bg-white/60"><Plus className="size-3.5 text-[#d75a41]" />new key</button>
        {keys.map((item) => (
          <button key={item.key} onClick={() => void readKey(item.key)} className={cn("flex w-full items-center gap-2 px-4 py-2 text-left font-mono text-[10px] transition", key === item.key ? "bg-[#e5e0d6] font-bold text-[#35413e]" : "text-[#848a83] hover:bg-white/60 hover:text-[#4c5853]")}>
            <KeyRound className="size-3.5 text-[#668e7a]" />
            <span className="min-w-0 flex-1 truncate">{item.key}</span>
            <span className="text-[9px] text-[#a0a39c]">{formatBytes(item.size)}</span>
          </button>
        ))}
        {!keys.length && <p className="px-4 py-8 text-center font-mono text-[9px]   text-[#a1a49e]">No keys yet</p>}
        {status && <p className="mx-4 mt-3 rounded-md border border-[#ded8cd] bg-white/55 px-3 py-2 font-mono text-[10px]   text-[#727a74]">{status}</p>}
      </aside>
      <div className="p-5">
        <form className="flex flex-col gap-3 sm:flex-row" onSubmit={(event) => { event.preventDefault(); void readKey(); }}>
          <div className="flex min-w-0 flex-1 items-center gap-2 rounded-md border border-[#d6d0c3] bg-white/75 px-3">
            <Search className="size-4 text-[#959a93]" />
            <Input required value={key} onChange={(event) => setKey(event.target.value)} placeholder="visits" className="h-10 border-0 bg-transparent p-0 focus:ring-0" />
          </div>
          <Button type="submit" variant="outline" disabled={loading}><Search className="size-3.5" />Read</Button>
        </form>
        <div className="mt-4 overflow-hidden rounded-lg border border-[#d9d3c7] bg-[#202b29]">
          <div className="flex items-center justify-between border-b border-white/10 px-4 py-3">
            <p className="font-mono text-[10px] text-[#b5c1bb]">{key.trim() || "Select a key"}</p>
            <span className="font-mono text-[9px]   text-[#778781]">{value.length} bytes</span>
          </div>
          <textarea value={value} onChange={(event) => setValue(event.target.value)} spellCheck={false} className="min-h-80 w-full resize-y bg-transparent p-4 font-mono text-[11px] leading-6 text-[#d8dfd8] outline-none" placeholder="Value" />
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={() => void deleteKey()} disabled={loading || !key.trim()}><Trash2 className="size-3.5" />Delete</Button>
          <Button type="button" onClick={() => void writeKey()} disabled={loading || !key.trim()}><Save className="size-3.5" />Save</Button>
        </div>
      </div>
    </div>
  );
}
