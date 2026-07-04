import { type FormEvent, useDeferredValue, useEffect, useState } from "react";
import { Archive, ArrowLeft, Globe2, KeyRound, Pencil, Plus, RefreshCw, Search, Trash2, Waypoints } from "lucide-react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { errorText, fetchJSON } from "../app/api";
import type { WorkerKVKey } from "../app/types";
import { formatBytes, sortNamespaces } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { KVKeyDialog } from "../components/dialogs/kv-key-dialog";
import { Field, Panel, WorkerDetailEmpty } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { cn } from "../lib/utils";

export function KVNamespaceDetailPage() {
  const navigate = useNavigate();
  const { namespaceId } = useParams();
  const { namespaces } = useWorkspace();
  const namespace = namespaces.find((item) => item.id === namespaceId);

  if (!namespace) return <Navigate to="/kv" replace />;

  return <KVNamespaceDetailContent namespace={namespace} onBack={() => navigate("/kv")} />;
}

function KVNamespaceDetailContent({
  namespace,
  onBack,
}: {
  namespace: { id: string; name: string; created_at: string };
  onBack: () => void;
}) {
  const { workers, setNamespaces, notify, apiConnected } = useWorkspace();
  const [name, setName] = useState(namespace.name);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [keys, setKeys] = useState<WorkerKVKey[]>([]);
  const [keysLoading, setKeysLoading] = useState(false);
  const [keysStatus, setKeysStatus] = useState("");
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogMode, setDialogMode] = useState<"create" | "edit">("create");
  const [draftKey, setDraftKey] = useState("");
  const [draftValue, setDraftValue] = useState("");
  const [originalKey, setOriginalKey] = useState("");
  const [valueLoading, setValueLoading] = useState(false);
  const [submittingKey, setSubmittingKey] = useState(false);
  const [deletingKey, setDeletingKey] = useState("");
  const bindings = workers.flatMap((worker) =>
    (worker.kv_bindings ?? [])
      .filter((binding) => binding.id === namespace.id)
      .map((binding) => ({ worker, binding })),
  );
  const accessorWorkers = bindings.map(({ worker }) => worker).filter((worker, index, all) => all.findIndex((candidate) => candidate.id === worker.id) === index);
  const [accessorWorkerID, setAccessorWorkerID] = useState(accessorWorkers[0]?.id ?? "");

  useEffect(() => {
    setName(namespace.name);
  }, [namespace.id, namespace.name]);

  useEffect(() => {
    setAccessorWorkerID((current) => current && accessorWorkers.some((worker) => worker.id === current) ? current : (accessorWorkers[0]?.id ?? ""));
  }, [namespace.id, accessorWorkers]);

  const activeWorker = accessorWorkers.find((worker) => worker.id === accessorWorkerID);
  const namespaceBase = activeWorker ? `/v1/apps/${activeWorker.id}/kv/namespaces/${encodeURIComponent(namespace.id)}` : "";
  const filteredKeys = keys.filter(({ key }) => key.toLowerCase().includes(deferredSearch.trim().toLowerCase()));

  useEffect(() => {
    setKeys([]);
    setKeysStatus("");
    setSearch("");
    setDialogOpen(false);
  }, [namespace.id, accessorWorkerID]);

  useEffect(() => {
    if (!namespaceBase) {
      setKeys([]);
      setKeysStatus(accessorWorkers.length ? "Choose a worker to inspect this namespace." : "Bind this namespace to a worker to inspect its keys.");
      return;
    }

    let cancelled = false;

    async function loadKeys() {
      setKeysLoading(true);
      setKeysStatus("");
      try {
        const nextKeys = await fetchJSON<WorkerKVKey[]>(namespaceBase);
        if (cancelled) return;
        setKeys(nextKeys);
        setKeysStatus(nextKeys.length ? "Keys refreshed" : "No keys in this namespace yet.");
      } catch (error) {
        if (cancelled) return;
        setKeys([]);
        setKeysStatus(error instanceof Error ? error.message : "KV list failed");
      } finally {
        if (!cancelled) setKeysLoading(false);
      }
    }

    void loadKeys();
    return () => {
      cancelled = true;
    };
  }, [namespaceBase, accessorWorkers.length]);

  function closeDialog() {
    setDialogOpen(false);
    setDialogMode("create");
    setDraftKey("");
    setDraftValue("");
    setOriginalKey("");
    setValueLoading(false);
  }

  async function refreshKeys() {
    if (!namespaceBase) return;
    setKeysLoading(true);
    setKeysStatus("");
    try {
      const nextKeys = await fetchJSON<WorkerKVKey[]>(namespaceBase);
      setKeys(nextKeys);
      setKeysStatus(nextKeys.length ? "Keys refreshed" : "No keys in this namespace yet.");
    } catch (error) {
      setKeys([]);
      setKeysStatus(error instanceof Error ? error.message : "KV list failed");
    } finally {
      setKeysLoading(false);
    }
  }

  function openCreateDialog() {
    setDialogMode("create");
    setDraftKey("");
    setDraftValue("");
    setOriginalKey("");
    setValueLoading(false);
    setDialogOpen(true);
  }

  async function openEditDialog(nextKey: string) {
    if (!namespaceBase) return;
    setDialogMode("edit");
    setDraftKey(nextKey);
    setDraftValue("");
    setOriginalKey(nextKey);
    setValueLoading(true);
    setDialogOpen(true);
    try {
      const response = await fetch(`${namespaceBase}/${encodeURIComponent(nextKey)}`);
      if (response.status === 404) {
        setDialogOpen(false);
        notify("Key not found");
        return;
      }
      if (!response.ok) throw new Error(`KV read failed (${response.status})`);
      setDraftValue(await response.text());
    } catch (error) {
      setDialogOpen(false);
      notify(error instanceof Error ? error.message : "KV read failed");
    } finally {
      setValueLoading(false);
    }
  }

  async function submitKey(event: FormEvent) {
    event.preventDefault();
    if (!namespaceBase) return;
    const trimmedKey = draftKey.trim();
    if (!trimmedKey) return notify("Key is required");

    setSubmittingKey(true);
    try {
      if (dialogMode === "edit" && originalKey && originalKey !== trimmedKey) {
        const deleteResponse = await fetch(`${namespaceBase}/${encodeURIComponent(originalKey)}`, { method: "DELETE" });
        if (!deleteResponse.ok) throw new Error(`KV rename failed (${deleteResponse.status})`);
      }

      const response = await fetch(`${namespaceBase}/${encodeURIComponent(trimmedKey)}`, {
        method: "PUT",
        body: draftValue,
      });
      if (!response.ok) throw new Error(`KV write failed (${response.status})`);

      await refreshKeys();
      closeDialog();
      notify(dialogMode === "edit" ? `${trimmedKey} updated` : `${trimmedKey} created`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "KV write failed");
    } finally {
      setSubmittingKey(false);
    }
  }

  async function removeKey(nextKey: string) {
    if (!namespaceBase) return;
    if (!window.confirm(`Delete key "${nextKey}" from ${namespace.name}?`)) return;
    setDeletingKey(nextKey);
    try {
      const response = await fetch(`${namespaceBase}/${encodeURIComponent(nextKey)}`, { method: "DELETE" });
      if (!response.ok) throw new Error(`KV delete failed (${response.status})`);
      await refreshKeys();
      notify(`${nextKey} deleted`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "KV delete failed");
    } finally {
      setDeletingKey("");
    }
  }

  async function saveNamespace() {
    const trimmed = name.trim();
    if (!trimmed) return notify("Namespace name is required");
    setSaving(true);
    try {
      let nextNamespace = { ...namespace, name: trimmed };
      if (apiConnected) {
        const response = await fetch(`/v1/kv/namespaces/${encodeURIComponent(namespace.id)}`, {
          method: "PATCH",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ name: trimmed }),
        });
        if (!response.ok) throw new Error(`Namespace update failed (${response.status})`);
        nextNamespace = await response.json();
      }
      setNamespaces((current) => sortNamespaces(current.map((item) => item.id === namespace.id ? nextNamespace : item)));
      notify(`${trimmed} updated`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Namespace update failed");
    } finally {
      setSaving(false);
    }
  }

  async function deleteNamespace() {
    if (!window.confirm(`Delete namespace "${namespace.name}"?`)) return;
    setDeleting(true);
    try {
      if (apiConnected) {
        const response = await fetch(`/v1/kv/namespaces/${encodeURIComponent(namespace.id)}`, { method: "DELETE" });
        if (!response.ok) throw new Error(await errorText(response, `Namespace delete failed (${response.status})`));
      }
      setNamespaces((current) => current.filter((item) => item.id !== namespace.id));
      notify(`${namespace.name} deleted`);
      onBack();
    } catch (error) {
      notify(error instanceof Error ? error.message : "Namespace delete failed");
    } finally {
      setDeleting(false);
    }
  }

  const bindingCount = bindings.length;
  const cards = [
    { label: "Bindings", value: String(bindingCount), note: "active namespace references", icon: Waypoints },
    { label: "Workers", value: String(accessorWorkers.length), note: "workers with live access", icon: Globe2 },
    { label: "Created", value: new Date(namespace.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" }), note: "namespace birthday", icon: Archive },
  ];

  return (
    <>
      <button onClick={onBack} className="animate-rise mb-5 flex items-center gap-2 font-mono text-[10px] font-bold   text-[#77817a] transition hover:text-[#d75a41]"><ArrowLeft className="size-3.5" />All namespaces</button>
      <div className="animate-rise mb-6 flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <h1 className="text-4xl text-[#26332f] md:text-5xl">{namespace.name}</h1>
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 font-mono text-[10px] text-[#858b84]"><span className="flex items-center gap-1.5"><KeyRound className="size-3" />{namespace.id}</span></div>
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => void deleteNamespace()} disabled={deleting || saving}><Trash2 className="size-3.5" />Delete</Button>
          <Button onClick={() => void saveNamespace()} disabled={saving || deleting || !name.trim()}><Archive className="size-3.5" />Save</Button>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        {cards.map(({ label, value, note, icon: Icon }, index) => <div key={label} style={{ animationDelay: `${index * 60}ms` }} className="paper-panel animate-rise rounded-lg border border-[#dcd6ca] bg-[#fbf9f3]/85 p-4"><div className="flex items-center justify-between"><p className="font-mono text-[9px]   text-[#90958e]">{label}</p><Icon className="size-3.5 text-[#d75a41]" /></div><p className="mt-3  text-3xl ">{value}</p><p className="mt-1 font-mono text-[9px]   text-[#999d97]">{note}</p></div>)}
      </div>

      <div className="mt-6 grid gap-6 xl:grid-cols-[1.55fr_1fr]">
        <section className="paper-panel animate-rise overflow-hidden rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85">
          <header className="border-b border-[#e7e1d6] px-5 py-4">
            <h2 className="text-sm font-extrabold">Edit namespace</h2>
          </header>
          <div className="p-5">
            <Field label="Name"><Input value={name} onChange={(event) => setName(event.target.value)} placeholder="shared-cache" /></Field>
            <div className="mt-4 overflow-hidden rounded-lg border border-[#e2ddd2]">
              {[
                ["Namespace ID", namespace.id],
                ["Created", new Date(namespace.created_at).toLocaleString()],
              ].map(([label, value]) => (
                <div key={label} className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]">
                  <span className="font-mono text-[10px]   text-[#93978f]">{label}</span>
                  <span className="font-mono text-[11px] font-bold text-[#4f5a55] break-all">{value}</span>
                </div>
              ))}
            </div>
          </div>
        </section>

        <Panel title="Bound workers">
          {bindings.length ? (
            <div className="space-y-3">
              {bindings.map(({ worker, binding }) => (
                <div key={`${worker.id}-${binding.binding}`} className="rounded-lg border border-[#e2ddd2] bg-white/45 px-4 py-3">
                  <div className="flex items-center justify-between gap-3">
                    <div>
                      <p className="text-xs font-extrabold text-[#35413e]">{worker.name}</p>
                      <p className="mt-1 font-mono text-[10px] text-[#7d837d]">{worker.hostname}</p>
                    </div>
                    <Badge tone="green">{binding.binding}</Badge>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm leading-6 text-[#7a8079]">This namespace is not bound by any active deployment yet, so there is no worker path available for key inspection.</p>
          )}
        </Panel>
      </div>

      <section className="paper-panel animate-rise mt-6 overflow-hidden rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-[#e7e1d6] px-5 py-4">
          <div>
            <h2 className="text-sm font-extrabold">Namespace keys</h2>
          </div>
        </header>
        {activeWorker ? (
          <div className="p-5">
            <div className="mb-4 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex min-w-0 flex-1 items-center gap-2 rounded-md border border-[#d6d0c3] bg-white/75 px-3">
                <Search className="size-4 text-[#959a93]" />
                <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search keys" className="h-10 border-0 bg-transparent p-0 focus:ring-0" />
              </div>
              <div className="flex gap-2">
                <Button type="button" variant="outline" onClick={() => void refreshKeys()} disabled={keysLoading}><RefreshCw className={cn("size-3.5", keysLoading && "animate-spin")} />Refresh</Button>
                <Button type="button" onClick={openCreateDialog}><Plus className="size-3.5" />New key</Button>
              </div>
            </div>

            <div className="overflow-hidden rounded-xl border border-[#ddd7cb] bg-white/55">
              <div className="overflow-x-auto">
                <table className="w-full min-w-[720px] text-left">
                  <thead>
                    <tr className="border-b border-[#e5dfd4] font-mono text-[9px]   text-[#989b95]">
                      <th className="px-5 py-3">Key</th>
                      <th className="py-3">Size</th>
                      <th className="py-3">Worker</th>
                      <th className="pr-5 py-3 text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {filteredKeys.map((item) => (
                      <tr key={item.key} className="border-b border-[#ece7dc] text-xs transition last:border-0 hover:bg-white/70">
                        <td className="px-5 py-4">
                          <div>
                            <p className="font-extrabold text-[#35413e]">{item.key}</p>
                            <p className="mt-1 font-mono text-[10px] text-[#949891]">stored in {namespace.name}</p>
                          </div>
                        </td>
                        <td className="py-4 font-mono text-[10px] text-[#727a74]">{formatBytes(item.size)}</td>
                        <td className="py-4 text-[#7d837d]">{activeWorker.name}</td>
                        <td className="pr-5 py-4">
                          <div className="flex justify-end gap-2">
                            <Button type="button" variant="ghost" onClick={() => void openEditDialog(item.key)} disabled={keysLoading || deletingKey === item.key}><Pencil className="size-3.5" />Edit</Button>
                            <Button type="button" variant="ghost" onClick={() => void removeKey(item.key)} disabled={keysLoading || deletingKey === item.key}><Trash2 className="size-3.5" />Delete</Button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {!filteredKeys.length && (
                <div className="grid h-48 place-items-center border-t border-[#ece7dc] bg-[#fcfaf5]/80 text-center">
                  <div>
                    <KeyRound className="mx-auto size-5 text-[#b7b4ac]" />
                    <p className="mt-3 text-xs font-extrabold text-[#777e78]">{keys.length ? "No matching keys" : "No keys yet"}</p>
                    <p className="mt-1 font-mono text-[9px]   text-[#a1a49e]">
                      {keys.length ? "Adjust the search or create a new entry" : "Create the first entry for this namespace"}
                    </p>
                  </div>
                </div>
              )}
            </div>

            {/* <div className="mt-3 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-[#e2ddd2] bg-white/45 px-4 py-3">
              <p className="font-mono text-[10px]   text-[#727a74]">
                {activeWorker.name}
              </p>
              {keysStatus && <p className="font-mono text-[10px]   text-[#8a8f89]">{keysStatus}</p>}
            </div> */}
          </div>
        ) : (
          <WorkerDetailEmpty icon={<KeyRound />} title="No worker access yet" copy="Bind this namespace to a live worker deployment to read or write shared keys." />
        )}
      </section>

      <KVKeyDialog
        open={dialogOpen}
        mode={dialogMode}
        keyName={draftKey}
        value={draftValue}
        loading={valueLoading}
        submitting={submittingKey}
        onClose={closeDialog}
        onKeyNameChange={setDraftKey}
        onValueChange={setDraftValue}
        onSubmit={(event) => void submitKey(event)}
      />
    </>
  );
}
