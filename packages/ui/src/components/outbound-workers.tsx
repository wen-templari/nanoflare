import type { components } from "@nanoflare/schema";

import { Banner, Field, LayerCard, Table, Text } from "@cloudflare/kumo";
import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";

import { apiClient, errorMessage } from "../app/api";
import { useAuth } from "../app/auth-context";
import { Button } from "./ui/button";
import { Dialog } from "./ui/dialog";
import { Input } from "./ui/input";

type Deployment = components["schemas"]["WorkerDeployment"];
type Bundle = components["schemas"]["DeployInput"];
type DispatchBinding = NonNullable<Bundle["dispatch_namespaces"]>[number];
type Worker = components["schemas"]["App"];

export function DispatchBindingSummary({
  workerID,
  revision,
  onPublished,
}: {
  workerID: string;
  revision: number;
  onPublished?: () => void;
}) {
  const { activeOrgID, organizations } = useAuth();
  const canReadNamespaces =
    organizations.find((org) => org.id === activeOrgID)?.scopes?.includes("namespaces:read") ??
    false;
  const scopes = organizations.find((org) => org.id === activeOrgID)?.scopes ?? [];
  const canEdit = scopes.includes("deployments:write") && scopes.includes("namespaces:dispatch");
  const [editing, setEditing] = useState(false);
  const [updated, setUpdated] = useState(0);
  const [namespaceNames, setNamespaceNames] = useState<Record<string, string>>({});
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [workers, setWorkers] = useState<Worker[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    setDeployments([]);
    setNamespaceNames({});
    void Promise.all([
      apiClient.GET("/v1/organizations/{orgID}/workers/{workerID}/active-bindings", {
        params: { path: { orgID: activeOrgID, workerID } },
      }),
      apiClient.GET("/v1/organizations/{orgID}/workers", {
        params: { path: { orgID: activeOrgID } },
      }),
      canReadNamespaces
        ? apiClient.GET("/v1/organizations/{orgID}/dispatch-namespaces", {
            params: { path: { orgID: activeOrgID } },
          })
        : Promise.resolve({ data: [] }),
    ])
      .then(([bindings, apps, namespaces]) => {
        if (!current) return;
        if (bindings.error || apps.error)
          throw new Error(
            errorMessage(bindings.error || apps.error, "Dispatch bindings unavailable"),
          );
        setDeployments(bindings.data ?? []);
        setWorkers(apps.data ?? []);
        setNamespaceNames(
          Object.fromEntries(
            (namespaces.data ?? []).map((namespace) => [namespace.id, namespace.name]),
          ),
        );
      })
      .catch((err) => {
        if (current) setError(String(err));
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [activeOrgID, workerID, revision, updated, canReadNamespaces]);
  const rows = deployments.flatMap((deployment) =>
    (deployment.dispatch_namespaces ?? []).map((binding) => ({ deployment, binding })),
  );
  return (
    <section className="grid min-w-0 gap-3">
      <div className="grid gap-1.5">
        <Text as="h2" variant="heading3">
          Dispatch bindings
        </Text>
        <Text variant="secondary">
          Outbound Workers apply to each dispatcher binding. Editing creates a new revision using
          the current code, assets, variables and secret references.
        </Text>
      </div>
      {canEdit && rows.length > 0 && (
        <div>
          <Button
            variant="outline"
            disabled={
              loading || deployments.length !== 1 || deployments[0]?.traffic_percent !== 100
            }
            onClick={() => setEditing(true)}
          >
            Edit outbound Workers
          </Button>
          {deployments.length > 1 && (
            <Text variant="secondary">Select one revision for 100% of traffic before editing.</Text>
          )}
        </div>
      )}
      <EditOutboundBindings
        open={editing}
        workerID={workerID}
        deployment={deployments.length === 1 ? deployments[0] : undefined}
        onClose={() => setEditing(false)}
        onSaved={() => {
          setEditing(false);
          setUpdated((value) => value + 1);
          onPublished?.();
        }}
      />
      {error && <Banner variant="error">{error}</Banner>}
      <LayerCard className="min-w-0 overflow-x-auto">
        <Table>
          <Table.Header>
            <Table.Row>
              <Table.Head>Binding</Table.Head>
              <Table.Head>Namespace</Table.Head>
              <Table.Head>Outbound Worker</Table.Head>
              <Table.Head>Parameters</Table.Head>
              <Table.Head>Revision / traffic</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {rows.map(({ binding, deployment }) => {
              const target = workers.find((worker) => worker.id === binding.outbound?.worker_id);
              return (
                <Table.Row key={`${deployment.id}:${binding.binding}`}>
                  <Table.Cell>{binding.binding}</Table.Cell>
                  <Table.Cell>
                    <Link
                      className="text-kumo-link underline"
                      to={`/platforms/${binding.namespace_id}`}
                      title={
                        namespaceNames[binding.namespace_id] ?? binding.namespace_id.slice(0, 12)
                      }
                    >
                      {namespaceNames[binding.namespace_id] ?? binding.namespace_id.slice(0, 12)}
                    </Link>
                  </Table.Cell>
                  <Table.Cell>
                    {binding.outbound ? (
                      <div className="grid gap-1.5">
                        {target ? (
                          <Link className="text-kumo-link underline" to={`/workers/${target.id}`}>
                            {target.name}
                          </Link>
                        ) : (
                          <span>Unavailable Worker ({binding.outbound.worker_id})</span>
                        )}
                        {binding.outbound.entrypoint && (
                          <Text variant="secondary">Entrypoint: {binding.outbound.entrypoint}</Text>
                        )}
                        <Text variant="secondary">Customer TCP sockets disabled</Text>
                      </div>
                    ) : (
                      "None"
                    )}
                  </Table.Cell>
                  <Table.Cell>{binding.outbound?.parameters?.join(", ") || "None"}</Table.Cell>
                  <Table.Cell>
                    {deployment.id.slice(0, 8)} / {deployment.traffic_percent}%
                  </Table.Cell>
                </Table.Row>
              );
            })}
            {!rows.length && (
              <Table.Row>
                <Table.Cell colSpan={5}>
                  {loading ? "Loading dispatch bindings…" : "No active dispatch bindings."}
                </Table.Cell>
              </Table.Row>
            )}
          </Table.Body>
        </Table>
      </LayerCard>
    </section>
  );
}

export function OutboundBundleEditor({
  bundle,
  workerID,
  onChange,
  disabled = false,
}: {
  disabled?: boolean;
  bundle: Bundle;
  workerID: string;
  onChange: (bundle: Bundle) => void;
}) {
  return (
    <OutboundBindingEditor
      workerID={workerID}
      bindings={bundle.dispatch_namespaces ?? []}
      disabled={disabled}
      onChange={(bindings) =>
        onChange({ ...bundle, idempotency_key: crypto.randomUUID(), dispatch_namespaces: bindings })
      }
    />
  );
}

function OutboundBindingEditor({
  bindings,
  workerID,
  onChange,
  disabled = false,
}: {
  bindings: DispatchBinding[];
  workerID: string;
  onChange: (bindings: DispatchBinding[]) => void;
  disabled?: boolean;
}) {
  const { activeOrgID, organizations } = useAuth();
  const [workers, setWorkers] = useState<Worker[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const canDispatch =
    organizations.find((org) => org.id === activeOrgID)?.scopes?.includes("namespaces:dispatch") ??
    false;
  useEffect(() => {
    let current = true;
    setWorkers([]);
    setError("");
    setLoading(true);
    void apiClient
      .GET("/v1/organizations/{orgID}/workers", { params: { path: { orgID: activeOrgID } } })
      .then((result) => {
        if (!current) return;
        if (result.error)
          throw new Error(errorMessage(result.error, "Could not load outbound Workers"));
        setWorkers(
          (result.data ?? []).filter((worker) => !worker.platform && worker.id !== workerID),
        );
      })
      .catch((err) => {
        if (current) setError(String(err));
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [activeOrgID, workerID]);
  function update(index: number, outbound: (typeof bindings)[number]["outbound"]) {
    onChange(bindings.map((binding, i) => (i === index ? { ...binding, outbound } : binding)));
  }
  if (!bindings.length) return null;
  return (
    <section className="grid gap-4">
      <div className="grid gap-1.5">
        <Text as="h3" variant="heading3">
          Outbound Workers
        </Text>
        <Text variant="secondary">
          Select a deployed modules Worker. The server checks target eligibility when publishing.
          Named entrypoints must export a WorkerEntrypoint with a fetch handler.
        </Text>
      </div>
      {error && <Banner variant="error">{error}</Banner>}
      {!canDispatch && (
        <Banner variant="default">
          Deploying dispatch bindings requires the namespaces:dispatch permission.
        </Banner>
      )}
      {bindings.map((binding, index) => (
        <div key={binding.binding} className="grid gap-3">
          <Field label={`${binding.binding} — outbound Worker`}>
            <select
              className="w-full rounded-md bg-kumo-base px-3 py-2 text-sm ring ring-kumo-line"
              disabled={disabled || loading || !canDispatch}
              value={binding.outbound?.worker_id ?? ""}
              onChange={(event) =>
                update(
                  index,
                  event.target.value
                    ? {
                        ...binding.outbound,
                        worker_id: event.target.value,
                        parameters: binding.outbound?.parameters ?? [],
                      }
                    : undefined,
                )
              }
            >
              <option value="">{loading ? "Loading Workers…" : "None"}</option>
              {binding.outbound &&
                !workers.some((worker) => worker.id === binding.outbound?.worker_id) && (
                  <option value={binding.outbound.worker_id}>
                    Unavailable Worker ({binding.outbound.worker_id})
                  </option>
                )}
              {workers.map((worker) => (
                <option key={worker.id} value={worker.id}>
                  {worker.name}
                </option>
              ))}
            </select>
          </Field>
          {binding.outbound && (
            <>
              <Field label={`${binding.binding} — entrypoint (optional)`}>
                <Input
                  aria-label={`${binding.binding} — entrypoint (optional)`}
                  value={binding.outbound.entrypoint ?? ""}
                  disabled={disabled || !canDispatch}
                  placeholder="Default export"
                  onChange={(event) =>
                    update(index, {
                      ...binding.outbound!,
                      entrypoint: event.target.value || undefined,
                    })
                  }
                />
              </Field>
              <Field label={`${binding.binding} — parameter names (comma separated)`}>
                <ParameterNamesInput
                  label={`${binding.binding} — parameter names (comma separated)`}
                  parameters={binding.outbound.parameters ?? []}
                  disabled={disabled || !canDispatch}
                  onChange={(parameters) => update(index, { ...binding.outbound!, parameters })}
                />
              </Field>
              <Text variant="secondary">
                Customer TCP sockets will be disabled for this binding.
              </Text>
            </>
          )}
        </div>
      ))}
    </section>
  );
}

function ParameterNamesInput({
  label,
  parameters,
  disabled,
  onChange,
}: {
  label: string;
  parameters: string[];
  disabled: boolean;
  onChange: (parameters: string[]) => void;
}) {
  const [text, setText] = useState(parameters.join(", "));
  const parse = (value: string) =>
    value
      .split(",")
      .map((name) => name.trim())
      .filter(Boolean);
  const canonical = JSON.stringify(parameters);
  useEffect(() => {
    setText((current) =>
      JSON.stringify(parse(current)) === canonical
        ? current
        : (JSON.parse(canonical) as string[]).join(", "),
    );
  }, [canonical]);
  return (
    <Input
      aria-label={label}
      value={text}
      disabled={disabled}
      onChange={(event) => {
        setText(event.target.value);
        onChange(parse(event.target.value));
      }}
    />
  );
}

function EditOutboundBindings({
  open,
  workerID,
  deployment,
  onClose,
  onSaved,
}: {
  open: boolean;
  workerID: string;
  deployment?: Deployment;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { activeOrgID } = useAuth();
  const [bindings, setBindings] = useState<DispatchBinding[]>([]);
  const [key, setKey] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setBindings(structuredClone(deployment?.dispatch_namespaces ?? []));
    setKey(crypto.randomUUID());
    setError("");
  }, [open, deployment]);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!deployment || saving) return;
    setSaving(true);
    setError("");
    try {
      const result = await apiClient.PUT(
        "/v1/organizations/{orgID}/workers/{workerID}/dispatch-bindings",
        {
          params: { path: { orgID: activeOrgID, workerID } },
          body: {
            dispatch_namespaces: bindings,
            expected_deployment_id: deployment.id,
            idempotency_key: key,
          },
        },
      );
      if (result.error)
        throw new Error(
          errorMessage(
            result.error,
            "Could not publish outbound bindings. Reload if the active revision changed.",
          ),
        );
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not publish outbound bindings");
    } finally {
      setSaving(false);
    }
  }
  return (
    <Dialog
      open={open}
      title="Edit outbound Workers"
      description="Publish a new revision using the current code and bindings. Changes require one active revision receiving all traffic."
      onClose={() => {
        if (!saving) onClose();
      }}
    >
      <form onSubmit={submit} className="grid gap-4 text-sm">
        {error && <Banner variant="error">{error}</Banner>}
        <OutboundBindingEditor
          bindings={bindings}
          workerID={workerID}
          disabled={saving}
          onChange={(next) => {
            setBindings(next);
            setKey(crypto.randomUUID());
          }}
        />
        <div className="flex justify-end gap-2">
          <Button variant="ghost" type="button" disabled={saving} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={saving} disabled={saving || !deployment}>
            Publish revision
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
