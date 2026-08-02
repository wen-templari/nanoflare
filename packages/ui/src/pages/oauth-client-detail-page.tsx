import type { ReactNode } from "react";

import {
  Banner,
  Button,
  Dialog,
  InputArea,
  Label,
  LayerCard,
  Table,
  Tabs,
  Text,
  Tooltip,
} from "@cloudflare/kumo";
import { Check, Copy, PlugZap, Settings, SquarePen, Trash2, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import type { OAuthClient, OAuthClientConnection } from "../app/types";

import { apiClient, errorMessage } from "../app/api";
import { useQueryTab } from "../app/use-query-tab";
import { useWorkspace } from "../app/workspace-context";
import { ConfirmDeleteDialog } from "../components/kumo/confirm-delete-dialog";
import { Badge } from "../components/ui/badge";
import { Input } from "../components/ui/input";

const oauthScopes = [
  "workers:read",
  "workers:write",
  "deployments:write",
  "secrets:write",
  "kv:read",
  "kv:write",
  "objects:read",
  "objects:write",
];
const emptyForm = { name: "", redirectURIs: "", scopes: [] as string[] };
const oauthClientDetailTabs = ["overview", "connections", "settings"] as const;

export function OAuthClientDetailPage() {
  const { clientId = "" } = useParams();
  const navigate = useNavigate();
  const { activeOrgID, notify } = useWorkspace();
  const [client, setClient] = useState<OAuthClient | null>(null);
  const [connections, setConnections] = useState<OAuthClientConnection[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [tab, setTab] = useQueryTab<(typeof oauthClientDetailTabs)[number]>(
    oauthClientDetailTabs,
    "overview",
  );
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState(emptyForm);
  const [error, setError] = useState("");
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");

  useEffect(() => {
    void refresh();
  }, [activeOrgID, clientId]);

  async function refresh() {
    if (!clientId) return;
    setLoading(true);
    try {
      const [clientResult, connectionsResult] = await Promise.all([
        apiClient.GET("/v1/organizations/{orgID}/oauth-clients/{clientID}", {
          params: { path: { orgID: activeOrgID, clientID: clientId } },
        }),
        apiClient.GET("/v1/organizations/{orgID}/oauth-clients/{clientID}/connections", {
          params: { path: { orgID: activeOrgID, clientID: clientId } },
        }),
      ]);
      if (clientResult.error || !clientResult.data || connectionsResult.error) {
        throw new Error(
          errorMessage(
            clientResult.error || connectionsResult.error,
            "Could not load OAuth client",
          ),
        );
      }
      const nextClient = clientResult.data as OAuthClient;
      const nextConnections = connectionsResult.data as OAuthClientConnection[] | null;
      if (nextClient.disabled) {
        setClient(null);
        setConnections([]);
        setError("");
        return;
      }
      setClient(nextClient);
      setConnections(nextConnections ?? []);
      setError("");
    } catch (err) {
      setClient(null);
      setConnections([]);
      setError(err instanceof Error ? err.message : "Could not load OAuth client");
    } finally {
      setLoading(false);
    }
  }

  async function copy(value: string, label: string) {
    await navigator.clipboard.writeText(value);
    notify(`${label} copied`);
  }

  function openEdit() {
    if (!client) return;
    setForm({
      name: client.name,
      redirectURIs: (client.redirect_uris ?? []).join("\n"),
      scopes: client.scopes ?? [],
    });
    setError("");
    setFormOpen(true);
  }

  async function submitClient() {
    if (!client) return;
    setSaving(true);
    const payload = {
      name: form.name,
      redirect_uris: form.redirectURIs
        .split(/\n+/)
        .map((value) => value.trim())
        .filter(Boolean),
      scopes: form.scopes,
    };
    try {
      const { error } = await apiClient.PATCH(
        "/v1/organizations/{orgID}/oauth-clients/{clientID}",
        {
          params: { path: { orgID: activeOrgID, clientID: client.client_id } },
          body: payload,
        },
      );
      if (error) throw new Error(errorMessage(error, "Could not update OAuth client"));
      setFormOpen(false);
      notify("OAuth client updated");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update OAuth client");
    } finally {
      setSaving(false);
    }
  }

  async function deleteClient() {
    if (!client) return;
    setDeleting(true);
    setDeleteError("");
    try {
      const { error } = await apiClient.DELETE(
        "/v1/organizations/{orgID}/oauth-clients/{clientID}",
        {
          params: { path: { orgID: activeOrgID, clientID: client.client_id } },
        },
      );
      if (error) throw new Error(errorMessage(error, "Could not delete OAuth client"));
      notify("OAuth client deleted");
      void navigate("/settings");
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : "Could not delete OAuth client");
    } finally {
      setDeleting(false);
    }
  }

  return (
    <>
      {error && <Banner className="mb-4" description={error} variant="error" />}

      {client && (
        <div className="flex flex-col gap-6">
          <Tabs
            className="inline-flex max-w-full"
            listClassName="max-w-full"
            tabs={[
              { label: "Overview", value: "overview" },
              { label: "Connections", value: "connections" },
              { label: "Settings", value: "settings" },
            ]}
            onValueChange={(value) => setTab(value as "overview" | "connections" | "settings")}
            value={tab}
            variant="segmented"
          />

          {tab === "overview" && (
            <>
              <div className="grid gap-4 md:grid-cols-3">
                <LayerCard className="p-4">
                  <SectionHeading title="Client ID" eyebrow="Registration" />
                  <div className="flex min-w-0 items-center gap-2">
                    <Text DANGEROUS_className="min-w-0 truncate" variant="mono">
                      {client.client_id}
                    </Text>
                    <CopyButton label="Client ID" value={client.client_id} onCopy={copy} />
                  </div>
                </LayerCard>
                <LayerCard className="p-4">
                  <SectionHeading title="Status" eyebrow="Lifecycle" />
                  <Badge tone="green">Active</Badge>
                  <Text DANGEROUS_className="mt-2 text-xs" variant="secondary">
                    Updated {new Date(client.updated_at).toLocaleString()}
                  </Text>
                </LayerCard>
                <LayerCard className="p-4">
                  <SectionHeading
                    title="Scopes"
                    eyebrow={`${(client.scopes ?? []).length} allowed`}
                  />
                  <ScopeBadges scopes={client.scopes ?? []} />
                </LayerCard>
              </div>

              <LayerCard className="p-4">
                <SectionHeading
                  title="Redirect URIs"
                  eyebrow={`${(client.redirect_uris ?? []).length} configured`}
                />
                <div className="flex flex-col gap-2">
                  {(client.redirect_uris ?? []).map((uri) => (
                    <div className="flex min-w-0 items-center gap-2" key={uri}>
                      <Text DANGEROUS_className="min-w-0 truncate text-xs" variant="mono-secondary">
                        {uri}
                      </Text>
                      <CopyButton label="Redirect URI" value={uri} onCopy={copy} />
                    </div>
                  ))}
                </div>
              </LayerCard>
            </>
          )}

          {tab === "connections" && (
            <div>
              <SectionHeading title="Connected" eyebrow={`${connections.length} active`} />
              <TableSurface>
                <div className="overflow-x-auto">
                  <Table className="min-w-[860px] table-fixed">
                    <Table.Header>
                      <Table.Row>
                        <Table.Head className="w-[30%]">User</Table.Head>
                        <Table.Head className="w-[30%]">Resource org</Table.Head>
                        <Table.Head className="w-[28%]">Granted scopes</Table.Head>
                        <Table.Head className="w-[12%]">Connected</Table.Head>
                      </Table.Row>
                    </Table.Header>
                    <Table.Body>
                      {connections.map((connection) => (
                        <Table.Row key={`${connection.user_id}-${connection.org_id}`}>
                          <Table.Cell className="w-[30%]">
                            <Text DANGEROUS_className="truncate font-medium">
                              {connection.user_email}
                            </Text>
                            <Text DANGEROUS_className="truncate text-xs" variant="mono-secondary">
                              {connection.user_id}
                            </Text>
                          </Table.Cell>
                          <Table.Cell className="w-[30%]">
                            <Text DANGEROUS_className="truncate font-medium">
                              {connection.org_name}
                            </Text>
                            <Text DANGEROUS_className="truncate text-xs" variant="mono-secondary">
                              {connection.org_id}
                            </Text>
                          </Table.Cell>
                          <Table.Cell className="w-[28%]">
                            <ScopeBadges scopes={connection.scopes ?? []} />
                          </Table.Cell>
                          <Table.Cell className="w-[12%]">
                            <Text DANGEROUS_className="truncate" size="sm" variant="secondary">
                              {new Date(connection.created_at).toLocaleDateString(undefined, {
                                month: "short",
                                day: "numeric",
                              })}
                            </Text>
                          </Table.Cell>
                        </Table.Row>
                      ))}
                    </Table.Body>
                  </Table>
                </div>
                {!loading && !connections.length && (
                  <EmptyState
                    icon={<PlugZap />}
                    title="No active connections"
                    copy="Approved users and resource organizations will appear here."
                  />
                )}
              </TableSurface>
            </div>
          )}

          {tab === "settings" && (
            <div className="flex flex-col gap-4">
              <LayerCard className="p-4">
                <SectionHeading title="Basic info" eyebrow="OAuth client" />
                <div className="overflow-hidden rounded-lg border border-[#e2ddd2]">
                  {[
                    ["Client ID", client.client_id],
                    ["Name", client.name],
                    ["Status", "Active"],
                    ["Created", new Date(client.created_at).toLocaleString()],
                    ["Updated", new Date(client.updated_at).toLocaleString()],
                    ["Redirect URIs", String((client.redirect_uris ?? []).length)],
                    ["Allowed scopes", String((client.scopes ?? []).length)],
                  ].map(([label, value]) => (
                    <div
                      key={label}
                      className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]"
                    >
                      <span className="font-mono text-[10px] text-[#93978f]">{label}</span>
                      <span className="break-all font-mono text-[0.9em] font-medium text-kumo-default">
                        {value}
                      </span>
                    </div>
                  ))}
                </div>
              </LayerCard>
              <LayerCard className="p-4">
                <SectionHeading title="Actions" eyebrow="Manage client" />
                <div className="grid gap-4 md:grid-cols-[1fr_auto] md:items-center">
                  <Text size="sm" variant="secondary">
                    Update this client registration, redirect URIs, and allowed scopes.
                  </Text>
                  <Button variant="outline" onClick={openEdit}>
                    <SquarePen className="size-4" />
                    Edit
                  </Button>
                </div>
              </LayerCard>
              <LayerCard className="p-4">
                <SectionHeading title="Danger zone" eyebrow="" />
                <div className="grid gap-4 md:grid-cols-[1fr_auto] md:items-center">
                  <div>
                    <Text as="h3" size="sm">
                      Delete client
                    </Text>
                    <Text DANGEROUS_className="mt-1" size="sm" variant="secondary">
                      Permanently remove this OAuth client so integrations can no longer authorize
                      through it.
                    </Text>
                  </div>
                  <Button
                    variant="destructive"
                    onClick={() => {
                      setDeleteError("");
                      setDeleteOpen(true);
                    }}
                  >
                    <Trash2 className="size-4" />
                    Delete client
                  </Button>
                </div>
              </LayerCard>
              <ConfirmDeleteDialog
                confirmLabel="Delete client"
                description="This action cannot be undone. Existing integrations will no longer be able to authorize through this client."
                errorMessage={deleteError}
                loading={deleting}
                onConfirm={deleteClient}
                onOpenChange={setDeleteOpen}
                open={deleteOpen}
                title="Delete OAuth client"
              />
            </div>
          )}
        </div>
      )}

      {!loading && !client && !error && (
        <EmptyState
          icon={<Settings />}
          title="Client not found"
          copy="This OAuth client is not owned by the active organization."
        />
      )}

      <Dialog.Root open={formOpen} onOpenChange={(open) => !open && setFormOpen(false)}>
        <Dialog className="p-6" size="xl">
          <div className="flex items-start justify-between gap-4">
            <Dialog.Title className="text-lg font-semibold">Edit OAuth client</Dialog.Title>
            <Dialog.Close
              render={(props) => (
                <Button {...props} aria-label="Close" shape="square" size="sm" variant="ghost">
                  <X className="size-4" />
                </Button>
              )}
            />
          </div>
          <div className="flex flex-col gap-4 pt-4">
            <Input
              label="Name"
              value={form.name}
              onChange={(event) => {
                const name = event.currentTarget.value;
                setForm((current) => ({ ...current, name }));
              }}
            />
            <InputArea
              label="Redirect URIs"
              rows={3}
              value={form.redirectURIs}
              onChange={(event) => {
                const redirectURIs = event.currentTarget.value;
                setForm((current) => ({ ...current, redirectURIs }));
              }}
            />
            <div className="grid gap-1.5">
              <Label htmlFor="oauth-client-scopes">Allowed scopes</Label>
              <select
                id="oauth-client-scopes"
                className="min-h-24 rounded-lg bg-kumo-base p-2 ring ring-kumo-line"
                multiple
                value={form.scopes}
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    scopes: [...event.currentTarget.selectedOptions].map((option) => option.value),
                  }))
                }
              >
                {oauthScopes.map((scope) => (
                  <option key={scope} value={scope}>
                    {scope}
                  </option>
                ))}
              </select>
            </div>
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setFormOpen(false)}>
                Cancel
              </Button>
              <Button loading={saving} onClick={submitClient}>
                <Check className="size-4" />
                Save changes
              </Button>
            </div>
          </div>
        </Dialog>
      </Dialog.Root>
    </>
  );
}

function ScopeBadges({ scopes }: { scopes: string[] }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {scopes.map((scope) => (
        <Badge key={scope} tone="blue">
          {scope}
        </Badge>
      ))}
    </div>
  );
}

function CopyButton({
  label,
  value,
  onCopy,
}: {
  label: string;
  value: string;
  onCopy: (value: string, label: string) => void;
}) {
  return (
    <Tooltip
      content={`Copy ${label.toLowerCase()}`}
      render={
        <Button
          aria-label={`Copy ${label.toLowerCase()}`}
          shape="square"
          variant="ghost"
          onClick={() => onCopy(value, label)}
        >
          <Copy size={14} />
        </Button>
      }
    />
  );
}

function SectionHeading({ title, eyebrow }: { title: string; eyebrow: string }) {
  return (
    <div className="mb-3">
      <Text size="xs" variant="secondary">
        {eyebrow}
      </Text>
      <Text as="h2" DANGEROUS_className="mt-0.5" variant="heading3">
        {title}
      </Text>
    </div>
  );
}

function TableSurface({ children }: { children: ReactNode }) {
  return <LayerCard className="overflow-hidden">{children}</LayerCard>;
}

function EmptyState({ icon, title, copy }: { icon: ReactNode; title: string; copy: string }) {
  return (
    <div className="flex h-[220px] items-center justify-center">
      <div className="flex flex-col items-center gap-1 text-center [&_svg]:size-6">
        {icon}
        <Text DANGEROUS_className="font-medium" size="sm">
          {title}
        </Text>
        <Text DANGEROUS_className="text-xs" variant="secondary">
          {copy}
        </Text>
      </div>
    </div>
  );
}
