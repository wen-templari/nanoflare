import type { components } from "@nanoflare/schema";

import { Banner, Field, LayerCard, Table, Text } from "@cloudflare/kumo";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { apiClient, errorMessage } from "../app/api";
import { useAuth } from "../app/auth-context";
import { useWorkspace } from "../app/workspace-context";
import { DeployPlatformBundle } from "../components/platform-runtime";
import { CopyableResourceID } from "../components/shared/copyable-resource-id";
import { PageHeading } from "../components/shared/primitives";
import { Button } from "../components/ui/button";
import { Dialog } from "../components/ui/dialog";
import { Input } from "../components/ui/input";

type Namespace = components["schemas"]["DispatchNamespace"];
type Worker = components["schemas"]["App"];
type Grant = components["schemas"]["NamespaceResourceGrant"];
const selectStyle = "w-full rounded-md bg-kumo-base px-3 py-2 text-sm ring ring-kumo-line";

export function DispatchNamespacesPage() {
  const { namespaceId } = useParams();
  const navigate = useNavigate();
  const { activeOrgID, organizations } = useAuth();
  const { notify } = useWorkspace();
  const scopes = organizations.find((org) => org.id === activeOrgID)?.scopes ?? [];
  const canNamespace = scopes.includes("namespaces:write");
  const canGrant = scopes.includes("namespaces:dispatch");
  const canWorker = scopes.includes("workers:write");
  const canDeploy = scopes.includes("deployments:write");
  const [namespaces, setNamespaces] = useState<Namespace[]>([]);
  const [scripts, setScripts] = useState<Worker[]>([]);
  const [resources, setResources] = useState<Record<string, { id: string; name: string }[]>>({});
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [revision, refresh] = useState(0);
  const [dialog, setDialog] = useState<
    "" | "namespace" | "rename" | "script" | "delete-namespace" | "delete-script"
  >("");
  const [name, setName] = useState("");
  const [customerMode, setCustomerMode] = useState("organization");
  const [customerID, setCustomerID] = useState("");
  const [execution, setExecution] = useState("dynamic");
  const [draftGrants, setDraftGrants] = useState<Grant[]>([]);
  const [grantKind, setGrantKind] = useState("kv");
  const [resourceID, setResourceID] = useState("");
  const [grantCustomer, setGrantCustomer] = useState("");
  const [targetWorker, setTargetWorker] = useState<Worker>();
  const [deployOpen, setDeployOpen] = useState(false);
  const selected = namespaces.find((item) => item.id === namespaceId);
  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    setNamespaces([]);
    setScripts([]);
    setDialog("");
    setTargetWorker(undefined);
    const path = { orgID: activeOrgID };
    void Promise.all([
      apiClient.GET("/v1/organizations/{orgID}/dispatch-namespaces", { params: { path } }),
      apiClient.GET("/v1/organizations/{orgID}/kv-namespaces", { params: { path } }),
      apiClient.GET("/v1/organizations/{orgID}/databases", { params: { path } }),
      apiClient.GET("/v1/organizations/{orgID}/object-storage-buckets", { params: { path } }),
    ])
      .then(([ns, kv, db, buckets]) => {
        if (!current) return;
        if (ns.error) throw new Error(errorMessage(ns.error, "Could not load namespaces"));
        setNamespaces(ns.data ?? []);
        setResources({ kv: kv.data ?? [], db: db.data ?? [], objects: buckets.data ?? [] });
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
  }, [activeOrgID, revision]);
  useEffect(() => {
    let current = true;
    setScripts([]);
    setDraftGrants(selected?.grants ?? []);
    setResourceID("");
    if (selected)
      void apiClient
        .GET("/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts", {
          params: { path: { orgID: activeOrgID, namespaceID: selected.id } },
        })
        .then((result) => {
          if (!current) return;
          if (result.error) setError(errorMessage(result.error, "Could not load private Workers"));
          else setScripts(result.data ?? []);
        })
        .catch((err) => {
          if (current) setError(String(err));
        });
    return () => {
      current = false;
    };
  }, [activeOrgID, selected]);
  function open(next: typeof dialog, worker?: Worker) {
    setName(next === "rename" ? (selected?.name ?? "") : "");
    setCustomerID("");
    setTargetWorker(worker);
    setError("");
    setDialog(next);
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      const path = { orgID: activeOrgID, namespaceID: selected?.id ?? "" };
      if (dialog === "namespace") {
        const result = await apiClient.POST("/v1/organizations/{orgID}/dispatch-namespaces", {
          params: { path: { orgID: activeOrgID } },
          body: { name: name.trim(), customer_mode: customerMode, trust: "trusted" },
        });
        if (result.error || !result.data)
          throw new Error(errorMessage(result.error, "Namespace creation failed"));
        void navigate(`/platforms/${result.data.id}`);
      } else if (dialog === "rename") {
        const result = await apiClient.PATCH(
          "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}",
          { params: { path }, body: { name: name.trim() } },
        );
        if (result.error) throw new Error(errorMessage(result.error, "Rename failed"));
      } else if (dialog === "script") {
        const result = await apiClient.POST(
          "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts",
          {
            params: { path },
            body: {
              script_name: name.trim(),
              customer_id: customerID.trim() || undefined,
              execution,
            },
          },
        );
        if (result.error) throw new Error(errorMessage(result.error, "Worker creation failed"));
      } else if (dialog === "delete-script" && targetWorker?.platform) {
        const result = await apiClient.DELETE(
          "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts/{scriptName}",
          { params: { path: { ...path, scriptName: targetWorker.platform.script_name } } },
        );
        if (result.error) throw new Error(errorMessage(result.error, "Worker deletion failed"));
      } else if (dialog === "delete-namespace") {
        const result = await apiClient.DELETE(
          "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}",
          { params: { path } },
        );
        if (result.error)
          throw new Error(
            errorMessage(
              result.error,
              "The namespace must have no scripts or grants before deletion",
            ),
          );
        void navigate("/platforms");
      }
      setDialog("");
      refresh((value) => value + 1);
      notify("Saved");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Operation failed");
    } finally {
      setSaving(false);
    }
  }
  function addGrant() {
    if (!resourceID || (selected?.customer_mode === "platform" && !grantCustomer.trim()))
      return setError("Choose a resource and customer ID.");
    const grant: Grant = {
      kind: grantKind,
      resource_id: resourceID,
      customer_id: selected?.customer_mode === "platform" ? grantCustomer.trim() : undefined,
    };
    if (
      !draftGrants.some(
        (item) =>
          item.kind === grant.kind &&
          item.resource_id === grant.resource_id &&
          item.customer_id === grant.customer_id,
      )
    )
      setDraftGrants([...draftGrants, grant]);
    setError("");
  }
  async function saveGrants() {
    if (!selected) return;
    setSaving(true);
    setError("");
    try {
      const result = await apiClient.PUT(
        "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/grants",
        {
          params: { path: { orgID: activeOrgID, namespaceID: selected.id } },
          body: { expected_revision: selected.policy_revision, grants: draftGrants },
        },
      );
      if (result.error)
        throw new Error(
          errorMessage(result.error, "Grant update failed. Refresh to resolve a policy conflict."),
        );
      refresh((value) => value + 1);
      notify("Resource grants saved");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Grant update failed");
    } finally {
      setSaving(false);
    }
  }
  const deleting = dialog === "delete-namespace" || dialog === "delete-script";
  return (
    <div className="text-sm">
      <PageHeading
        eyebrow="Workers"
        title={selected?.name ?? "Dispatch namespaces"}
        copy="Organize private customer Workers by platform environment. Only trusted code in a shared runtime is supported."
        actions={
          canNamespace && <Button onClick={() => open("namespace")}>Create namespace</Button>
        }
      />
      {error && (
        <Banner variant="error" className="mb-4">
          {error}
        </Banner>
      )}
      <div className="mb-6 flex flex-wrap items-end gap-3">
        <Field label="Namespace">
          <select
            aria-label="Namespace"
            className={selectStyle}
            value={selected?.id ?? ""}
            onChange={(event) =>
              void navigate(event.target.value ? `/platforms/${event.target.value}` : "/platforms")
            }
          >
            <option value="">Choose a namespace</option>
            {namespaces.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </select>
        </Field>
        <Button variant="outline" onClick={() => refresh((value) => value + 1)} disabled={loading}>
          Refresh
        </Button>
        {selected && canNamespace && (
          <>
            <Button variant="outline" onClick={() => open("rename")}>
              Rename
            </Button>
            <Button variant="ghost" onClick={() => open("delete-namespace")}>
              Delete namespace
            </Button>
          </>
        )}
      </div>
      {!selected ? (
        <LayerCard className="px-5 py-4">
          <Text>
            {loading
              ? "Loading namespaces…"
              : namespaces.length
                ? "Choose a namespace to manage its private Workers and customer grants."
                : "No dispatch namespaces yet."}
          </Text>
        </LayerCard>
      ) : (
        <div className="grid min-w-0 gap-7">
          <div className="flex min-w-0 flex-wrap gap-4">
            <Text>
              Customer identity:{" "}
              {selected.customer_mode === "platform" ? "Platform customer IDs" : "Organization"}
            </Text>
            <Text>Policy revision: {selected.policy_revision}</Text>
            <div className="min-w-0 max-w-full">
              <CopyableResourceID
                value={selected.id}
                onCopied={() => notify("Namespace ID copied")}
              />
            </div>
          </div>
          <section className="grid min-w-0 gap-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <Text as="h2" variant="heading3">
                Private Workers
              </Text>
              {canWorker && <Button onClick={() => open("script")}>Create private Worker</Button>}
            </div>
            <LayerCard className="min-w-0 overflow-x-auto">
              <Table>
                <Table.Header>
                  <Table.Row>
                    <Table.Head>Script</Table.Head>
                    <Table.Head>Customer</Table.Head>
                    <Table.Head>Execution</Table.Head>
                    <Table.Head>Actions</Table.Head>
                  </Table.Row>
                </Table.Header>
                <Table.Body>
                  {scripts.map((worker) => (
                    <Table.Row key={worker.id}>
                      <Table.Cell>
                        <Link className="text-kumo-link underline" to={`/workers/${worker.id}`}>
                          {worker.platform?.script_name}
                        </Link>
                      </Table.Cell>
                      <Table.Cell>{worker.platform?.customer_id || "Organization"}</Table.Cell>
                      <Table.Cell>{worker.platform?.execution || "static"}</Table.Cell>
                      <Table.Cell>
                        <div className="flex gap-2">
                          <Button
                            variant="outline"
                            disabled={!canDeploy}
                            onClick={() => {
                              setTargetWorker(worker);
                              setDeployOpen(true);
                            }}
                          >
                            Deploy
                          </Button>
                          <Button
                            variant="ghost"
                            disabled={!canWorker}
                            onClick={() => open("delete-script", worker)}
                          >
                            Delete
                          </Button>
                        </div>
                      </Table.Cell>
                    </Table.Row>
                  ))}
                  {!scripts.length && (
                    <Table.Row>
                      <Table.Cell colSpan={4}>No private Workers yet.</Table.Cell>
                    </Table.Row>
                  )}
                </Table.Body>
              </Table>
            </LayerCard>
          </section>
          <section className="grid min-w-0 gap-3">
            <Text as="h2" variant="heading3">
              Resource grants
            </Text>
            <Text variant="secondary">
              Bindings need a grant for the exact resource and customer. Sharing a resource requires
              a grant for each participating customer.
            </Text>
            <LayerCard className="min-w-0 overflow-x-auto">
              <Table>
                <Table.Header>
                  <Table.Row>
                    <Table.Head>Type</Table.Head>
                    <Table.Head>Resource</Table.Head>
                    <Table.Head>Customer</Table.Head>
                    <Table.Head>Actions</Table.Head>
                  </Table.Row>
                </Table.Header>
                <Table.Body>
                  {draftGrants.map((grant, index) => (
                    <Table.Row key={`${grant.kind}:${grant.resource_id}:${grant.customer_id}`}>
                      <Table.Cell>
                        {grant.kind === "kv"
                          ? "KV"
                          : grant.kind === "db"
                            ? "Database"
                            : "Object storage"}
                      </Table.Cell>
                      <Table.Cell>
                        {resources[grant.kind]?.find((item) => item.id === grant.resource_id)
                          ?.name ?? grant.resource_id}
                      </Table.Cell>
                      <Table.Cell>{grant.customer_id || "Organization"}</Table.Cell>
                      <Table.Cell>
                        <Button
                          variant="ghost"
                          disabled={!canGrant}
                          onClick={() =>
                            setDraftGrants(
                              draftGrants.filter((_, itemIndex) => itemIndex !== index),
                            )
                          }
                        >
                          Remove
                        </Button>
                      </Table.Cell>
                    </Table.Row>
                  ))}
                  {!draftGrants.length && (
                    <Table.Row>
                      <Table.Cell colSpan={4}>
                        No resource grants. Code without external resource bindings can still run.
                      </Table.Cell>
                    </Table.Row>
                  )}
                </Table.Body>
              </Table>
            </LayerCard>
            {canGrant && (
              <>
                <div className="grid items-end gap-3 sm:grid-cols-4">
                  <Field label="Resource type">
                    <select
                      className={selectStyle}
                      value={grantKind}
                      onChange={(event) => {
                        setGrantKind(event.target.value);
                        setResourceID("");
                      }}
                    >
                      <option value="kv">KV</option>
                      <option value="db">Database</option>
                      <option value="objects">Object storage</option>
                    </select>
                  </Field>
                  <Field label="Resource">
                    <select
                      aria-label="Grant resource"
                      className={selectStyle}
                      value={resourceID}
                      onChange={(event) => setResourceID(event.target.value)}
                    >
                      <option value="">Choose a resource</option>
                      {resources[grantKind]?.map((item) => (
                        <option key={item.id} value={item.id}>
                          {item.name}
                        </option>
                      ))}
                    </select>
                  </Field>
                  {selected.customer_mode === "platform" && (
                    <Field label="Customer ID">
                      <Input
                        aria-label="Grant customer ID"
                        value={grantCustomer}
                        onChange={(event) => setGrantCustomer(event.target.value)}
                      />
                    </Field>
                  )}
                  <Button variant="outline" type="button" onClick={addGrant}>
                    Add grant
                  </Button>
                </div>
                <div>
                  <Button loading={saving} disabled={saving} onClick={() => void saveGrants()}>
                    Save grants
                  </Button>
                </div>
              </>
            )}
          </section>
        </div>
      )}
      <Dialog
        open={dialog !== ""}
        onClose={() => setDialog("")}
        title={
          dialog === "namespace"
            ? "Create namespace"
            : dialog === "rename"
              ? "Rename namespace"
              : dialog === "script"
                ? "Create private Worker"
                : "Confirm deletion"
        }
        description={
          deleting
            ? `Delete ${dialog === "delete-script" ? targetWorker?.platform?.script_name : selected?.name}? This action cannot be undone.`
            : "Namespaces group environments; customer IDs and execution choices are immutable after script creation."
        }
      >
        <form className="grid gap-4 text-sm" onSubmit={submit}>
          {error && <Banner variant="error">{error}</Banner>}
          {!deleting && (
            <Field label={dialog === "script" ? "Script name" : "Name"}>
              <Input
                aria-label={dialog === "script" ? "Script name" : "Namespace name"}
                value={name}
                required
                onChange={(event) => setName(event.target.value)}
                pattern="[a-z0-9](?:[a-z0-9_]|-){0,62}"
              />
            </Field>
          )}
          {dialog === "namespace" && (
            <Field label="Customer identity">
              <select
                className={selectStyle}
                value={customerMode}
                onChange={(event) => setCustomerMode(event.target.value)}
              >
                <option value="organization">Organization is the customer</option>
                <option value="platform">Platform hosts multiple customers</option>
              </select>
            </Field>
          )}
          {dialog === "script" && (
            <>
              {selected?.customer_mode === "platform" && (
                <Field label="Customer ID">
                  <Input
                    aria-label="Worker customer ID"
                    required
                    value={customerID}
                    onChange={(event) => setCustomerID(event.target.value)}
                    pattern="[a-z0-9](?:[a-z0-9_]|-){0,62}"
                  />
                </Field>
              )}
              <Field label="Execution">
                <select
                  className={selectStyle}
                  value={execution}
                  onChange={(event) => setExecution(event.target.value)}
                >
                  <option value="dynamic">Private dynamic dispatch</option>
                  <option value="static">Existing static execution</option>
                </select>
              </Field>
            </>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setDialog("")}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant={deleting ? "danger" : "default"}
              disabled={saving}
              loading={saving}
            >
              {deleting ? "Delete" : "Save"}
            </Button>
          </div>
        </form>
      </Dialog>
      <DeployPlatformBundle
        workerID={targetWorker?.id ?? ""}
        open={deployOpen && !!targetWorker}
        onClose={() => setDeployOpen(false)}
        onSaved={() => {
          refresh((value) => value + 1);
          notify("Private Worker revision published");
        }}
      />
    </div>
  );
}
