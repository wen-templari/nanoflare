import { useEffect, useState } from "react";
import { Archive, ArrowLeft, Globe2, KeyRound, Trash2, Waypoints } from "lucide-react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { errorText } from "../app/api";
import { useWorkspace } from "../app/workspace-context";
import { sortNamespaces } from "../app/utils";
import { NamespaceKeyEditor } from "../components/shared/namespace-key-editor";
import { Field, Panel, WorkerDetailEmpty } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";

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
  const navigate = useNavigate();
  const { workers, setNamespaces, notify, apiConnected } = useWorkspace();
  const [name, setName] = useState(namespace.name);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
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

  const activeWorker = accessorWorkers.find((worker) => worker.id === accessorWorkerID);
  const bindingCount = bindings.length;
  const cards = [
    { label: "Bindings", value: String(bindingCount), note: "active namespace references", icon: Waypoints },
    { label: "Workers", value: String(accessorWorkers.length), note: "workers with live access", icon: Globe2 },
    { label: "Created", value: new Date(namespace.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" }), note: "namespace birthday", icon: Archive },
  ];

  return (
    <>
      <button onClick={onBack} className="animate-rise mb-5 flex items-center gap-2 font-mono text-[10px] font-bold uppercase tracking-[0.14em] text-[#77817a] transition hover:text-[#d75a41]"><ArrowLeft className="size-3.5" />All namespaces</button>
      <div className="animate-rise mb-6 flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <div className="flex flex-wrap items-center gap-2"><p className="font-mono text-[10px] uppercase tracking-[0.2em] text-[#d75a41]">KV namespace</p><Badge tone={bindingCount ? "green" : "orange"}>{bindingCount ? "bound" : "unbound"}</Badge></div>
          <h1 className="font-display mt-2 text-4xl tracking-[-0.04em] text-[#26332f] md:text-5xl">{namespace.name}</h1>
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 font-mono text-[10px] text-[#858b84]"><span className="flex items-center gap-1.5"><KeyRound className="size-3" />{namespace.id}</span></div>
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => void deleteNamespace()} disabled={deleting || saving}><Trash2 className="size-3.5" />Delete</Button>
          <Button onClick={() => void saveNamespace()} disabled={saving || deleting || !name.trim()}><Archive className="size-3.5" />Save</Button>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        {cards.map(({ label, value, note, icon: Icon }, index) => <div key={label} style={{ animationDelay: `${index * 60}ms` }} className="paper-panel animate-rise rounded-lg border border-[#dcd6ca] bg-[#fbf9f3]/85 p-4"><div className="flex items-center justify-between"><p className="font-mono text-[9px] uppercase tracking-[0.14em] text-[#90958e]">{label}</p><Icon className="size-3.5 text-[#d75a41]" /></div><p className="mt-3 font-display text-3xl tracking-[-0.04em]">{value}</p><p className="mt-1 font-mono text-[9px] uppercase tracking-[0.08em] text-[#999d97]">{note}</p></div>)}
      </div>

      <div className="mt-6 grid gap-6 xl:grid-cols-[1.55fr_1fr]">
        <section className="paper-panel animate-rise overflow-hidden rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85">
          <header className="border-b border-[#e7e1d6] px-5 py-4">
            <p className="font-mono text-[9px] uppercase tracking-[0.18em] text-[#d35c45]">Identity</p>
            <h2 className="mt-1 text-sm font-extrabold">Edit namespace</h2>
          </header>
          <div className="p-5">
            <Field label="Name"><Input value={name} onChange={(event) => setName(event.target.value)} placeholder="shared-cache" /></Field>
            <div className="mt-4 overflow-hidden rounded-lg border border-[#e2ddd2]">
              {[
                ["Namespace ID", namespace.id],
                ["Created", new Date(namespace.created_at).toLocaleString()],
              ].map(([label, value]) => (
                <div key={label} className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]">
                  <span className="font-mono text-[10px] uppercase tracking-[0.1em] text-[#93978f]">{label}</span>
                  <span className="font-mono text-[11px] font-bold text-[#4f5a55] break-all">{value}</span>
                </div>
              ))}
            </div>
          </div>
        </section>

        <Panel title="Bound workers" eyebrow={bindingCount ? "live references" : "no active bindings"}>
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
            <p className="font-mono text-[9px] uppercase tracking-[0.18em] text-[#d35c45]">Data access</p>
            <h2 className="mt-1 text-sm font-extrabold">Namespace keys</h2>
          </div>
          {accessorWorkers.length > 0 && (
            <select value={accessorWorkerID} onChange={(event) => setAccessorWorkerID(event.target.value)} className="rounded-md border border-[#d6d0c3] bg-white/75 px-3 py-2 font-mono text-[10px] text-[#4c5853] outline-none">
              {accessorWorkers.map((worker) => <option key={worker.id} value={worker.id}>{worker.name}</option>)}
            </select>
          )}
        </header>
        {activeWorker ? (
          <NamespaceKeyEditor
            workerID={activeWorker.id}
            namespaces={[{ id: namespace.id, label: namespace.name }]}
            namespaceID={namespace.id}
            onNamespaceChange={() => undefined}
            namespaceDisabled
            notify={notify}
          />
        ) : (
          <WorkerDetailEmpty icon={<KeyRound />} title="No worker access yet" copy="Bind this namespace to a live worker deployment to read or write shared keys." />
        )}
      </section>
    </>
  );
}
