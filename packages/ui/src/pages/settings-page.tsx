import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { ActionIcon, Alert, Box, Center, Code, Group, Modal, MultiSelect, ScrollArea, Stack, Table, Text, TextInput, Textarea, Title, Tooltip } from "@mantine/core";
import { Ban, Check, Copy, Plus, RotateCcw, Settings, SquarePen } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { apiFetch, errorText, fetchJSON } from "../app/api";
import type { OAuthClient, OAuthClientCreated } from "../app/types";
import { useWorkspace } from "../app/workspace-context";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { PageHeading } from "../components/shared/primitives";

const oauthScopes = ["apps:read", "apps:write", "deployments:write", "secrets:write", "kv:read", "kv:write", "objects:read", "objects:write"];
const emptyForm = { name: "", redirectURIs: "", scopes: [] as string[] };

export function SettingsPage() {
  const navigate = useNavigate();
  const { activeOrgID, notify } = useWorkspace();
  const [clients, setClients] = useState<OAuthClient[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [editingClient, setEditingClient] = useState<OAuthClient | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [oneTimeSecret, setOneTimeSecret] = useState<OAuthClientCreated | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    void refresh();
  }, [activeOrgID]);

  const activeClients = useMemo(() => clients.filter((client) => !client.disabled), [clients]);

  async function refresh() {
    setLoading(true);
    try {
      const nextClients = await fetchJSON<OAuthClient[] | null>("/v1/oauth/clients");
      setClients(nextClients ?? []);
      setError("");
    } catch (err) {
      setClients([]);
      setError(err instanceof Error ? err.message : "Could not load OAuth settings");
    } finally {
      setLoading(false);
    }
  }

  function openCreate() {
    setEditingClient(null);
    setForm(emptyForm);
    setError("");
    setFormOpen(true);
  }

  function openEdit(client: OAuthClient) {
    setEditingClient(client);
    setForm({ name: client.name, redirectURIs: client.redirect_uris.join("\n"), scopes: client.scopes });
    setError("");
    setFormOpen(true);
  }

  async function submitClient() {
    setSaving(true);
    const payload = {
      name: form.name,
      redirect_uris: form.redirectURIs.split(/\n+/).map((value) => value.trim()).filter(Boolean),
      scopes: form.scopes,
    };
    try {
      const response = await apiFetch(editingClient ? `/v1/oauth/clients/${editingClient.client_id}` : "/v1/oauth/clients", {
        method: editingClient ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!response.ok) throw new Error(await errorText(response, "Could not save OAuth client"));
      const saved = await response.json() as OAuthClient | OAuthClientCreated;
      if ("client_secret" in saved) setOneTimeSecret(saved);
      setFormOpen(false);
      notify(editingClient ? "OAuth client updated" : "OAuth client created");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save OAuth client");
    } finally {
      setSaving(false);
    }
  }

  async function rotateSecret(client: OAuthClient) {
    const response = await apiFetch(`/v1/oauth/clients/${client.client_id}/secret`, { method: "POST" });
    if (!response.ok) {
      setError(await errorText(response, "Could not rotate client secret"));
      return;
    }
    setOneTimeSecret(await response.json() as OAuthClientCreated);
    notify("OAuth client secret rotated");
    await refresh();
  }

  async function disableClient(client: OAuthClient) {
    const response = await apiFetch(`/v1/oauth/clients/${client.client_id}`, { method: "DELETE" });
    if (!response.ok) {
      setError(await errorText(response, "Could not disable OAuth client"));
      return;
    }
    notify("OAuth client disabled");
    await refresh();
  }

  async function copy(value: string, label: string) {
    await navigator.clipboard.writeText(value);
    notify(`${label} copied`);
  }

  return (
    <>
      <PageHeading
        eyebrow="Settings"
        title="Settings"
        copy="Register OAuth apps for this organization and review the external apps you have connected."
        actions={<Button onClick={openCreate}><Plus className="size-4" />New OAuth client</Button>}
      />

      {error && <Alert color="red" mb="md">{error}</Alert>}

      {oneTimeSecret && (
        <Alert color="blue" mb="md" title="Client secret shown once">
          <Group align="center" justify="space-between" wrap="nowrap">
            <Box>
              <Text size="sm">Store this secret now. Nanoflare will not show it again after this page refreshes.</Text>
              <Code mt={8} className="block break-all">{oneTimeSecret.client_secret}</Code>
            </Box>
            <Button variant="outline" onClick={() => copy(oneTimeSecret.client_secret, "Client secret")}><Copy className="size-4" />Copy</Button>
          </Group>
        </Alert>
      )}

      <Stack gap="lg">
        <Box>
          <SectionHeading title="OAuth clients" eyebrow={`${activeClients.length} active`} />
          <TableSurface>
            <ScrollArea>
              <Table highlightOnHover miw={900} verticalSpacing="sm" className="table-fixed">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th className="w-[28%]">Client</Table.Th>
                    <Table.Th className="w-[34%]">Client ID</Table.Th>
                    <Table.Th className="w-[18%]">Redirect URIs</Table.Th>
                    <Table.Th className="w-[10%]">Updated</Table.Th>
                    <Table.Th className="w-[10%]">Actions</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {clients.map((client) => (
                    <Table.Tr key={client.client_id} className="cursor-pointer" onClick={() => navigate(`/settings/oauth-clients/${client.client_id}`)}>
                      <Table.Td className="w-[28%]">
                        <Group gap="sm" wrap="nowrap">
                          <Text fw={700} truncate>{client.name}</Text>
                          <Badge tone={client.disabled ? "orange" : "green"}>{client.disabled ? "Disabled" : "Active"}</Badge>
                        </Group>
                      </Table.Td>
                      <Table.Td className="w-[34%]">
                        <Group gap="xs" wrap="nowrap">
                          <Text c="dimmed" ff="monospace" size="xs" truncate>{client.client_id}</Text>
                          <CopyButton label="Client ID" value={client.client_id} onCopy={copy} />
                        </Group>
                      </Table.Td>
                      <Table.Td className="w-[18%]">
                        <Stack gap={4}>{client.redirect_uris.map((uri) => <Text c="dimmed" ff="monospace" key={uri} size="xs" truncate>{uri}</Text>)}</Stack>
                      </Table.Td>
                      <Table.Td className="w-[10%]"><Text c="dimmed" size="sm" truncate>{new Date(client.updated_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Text></Table.Td>
                      <Table.Td className="w-[10%]">
                        <Group gap={4} wrap="nowrap">
                          <Tooltip label="Edit client"><ActionIcon aria-label="Edit client" variant="subtle" onClick={(event) => { event.stopPropagation(); openEdit(client); }} disabled={client.disabled}><SquarePen size={16} /></ActionIcon></Tooltip>
                          <Tooltip label="Rotate secret"><ActionIcon aria-label="Rotate secret" variant="subtle" onClick={(event) => { event.stopPropagation(); rotateSecret(client); }} disabled={client.disabled}><RotateCcw size={16} /></ActionIcon></Tooltip>
                          <Tooltip label="Disable client"><ActionIcon aria-label="Disable client" color="red" variant="subtle" onClick={(event) => { event.stopPropagation(); disableClient(client); }} disabled={client.disabled}><Ban size={16} /></ActionIcon></Tooltip>
                        </Group>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </ScrollArea>
            {!loading && !clients.length && <EmptyState icon={<Settings />} title="No OAuth clients" copy="Create one to let an external platform connect to Nanoflare." />}
          </TableSurface>
        </Box>
      </Stack>

      <Modal opened={formOpen} onClose={() => setFormOpen(false)} title={editingClient ? "Edit OAuth client" : "New OAuth client"} size="lg">
        <Stack>
          <TextInput label="Name" value={form.name} onChange={(event) => setForm((current) => ({ ...current, name: event.currentTarget.value }))} />
          <Textarea
            autosize
            label="Redirect URIs"
            minRows={3}
            value={form.redirectURIs}
            onChange={(event) => setForm((current) => ({ ...current, redirectURIs: event.currentTarget.value }))}
          />
          <MultiSelect
            data={oauthScopes}
            label="Allowed scopes"
            value={form.scopes}
            onChange={(scopes) => setForm((current) => ({ ...current, scopes }))}
          />
          <Group justify="end">
            <Button variant="ghost" onClick={() => setFormOpen(false)}>Cancel</Button>
            <Button loading={saving} onClick={submitClient}><Check className="size-4" />{editingClient ? "Save changes" : "Create client"}</Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

function CopyButton({ label, value, onCopy }: { label: string; value: string; onCopy: (value: string, label: string) => void }) {
  return (
    <Tooltip label={`Copy ${label.toLowerCase()}`}>
      <ActionIcon aria-label={`Copy ${label.toLowerCase()}`} size="sm" variant="subtle" onClick={() => onCopy(value, label)}>
        <Copy size={14} />
      </ActionIcon>
    </Tooltip>
  );
}

function SectionHeading({ title, eyebrow }: { title: string; eyebrow: string }) {
  return (
    <Box mb="sm">
      <Text c="dimmed" fw={700} size="xs" tt="uppercase">{eyebrow}</Text>
      <Title mt={2} order={3} size="h5">{title}</Title>
    </Box>
  );
}

function TableSurface({ children }: { children: ReactNode }) {
  return <Box bg="white" className="overflow-hidden rounded-lg border border-[var(--mantine-color-gray-3)]">{children}</Box>;
}

function EmptyState({ icon, title, copy }: { icon: ReactNode; title: string; copy: string }) {
  return <Center h={220}><Stack align="center" gap={4} ta="center" className="[&_svg]:size-6">{icon}<Text fw={700} size="sm">{title}</Text><Text c="dimmed" size="xs">{copy}</Text></Stack></Center>;
}
