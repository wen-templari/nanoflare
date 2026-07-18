import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { ActionIcon, Alert, Box, Center, Code, Group, Modal, MultiSelect, ScrollArea, SimpleGrid, Stack, Table, Text, Textarea, TextInput, Title, Tooltip } from "@mantine/core";
import { Check, Copy, PlugZap, Settings, SquarePen, Trash2 } from "lucide-react";
import { useNavigate, useParams } from "react-router-dom";
import { apiFetch, errorText, fetchJSON } from "../app/api";
import type { OAuthClient, OAuthClientConnection } from "../app/types";
import { useWorkspace } from "../app/workspace-context";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { PageHeading, Panel } from "../components/shared/primitives";

const oauthScopes = ["apps:read", "apps:write", "deployments:write", "secrets:write", "kv:read", "kv:write", "objects:read", "objects:write"];
const emptyForm = { name: "", redirectURIs: "", scopes: [] as string[] };

export function OAuthClientDetailPage() {
  const { clientId = "" } = useParams();
  const navigate = useNavigate();
  const { activeOrgID, notify } = useWorkspace();
  const [client, setClient] = useState<OAuthClient | null>(null);
  const [connections, setConnections] = useState<OAuthClientConnection[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState(emptyForm);
  const [error, setError] = useState("");

  useEffect(() => {
    void refresh();
  }, [activeOrgID, clientId]);

  async function refresh() {
    if (!clientId) return;
    setLoading(true);
    try {
      const [nextClient, nextConnections] = await Promise.all([
        fetchJSON<OAuthClient>(`/v1/oauth/clients/${clientId}`),
        fetchJSON<OAuthClientConnection[] | null>(`/v1/oauth/clients/${clientId}/connections`),
      ]);
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
    setForm({ name: client.name, redirectURIs: client.redirect_uris.join("\n"), scopes: client.scopes });
    setError("");
    setFormOpen(true);
  }

  async function submitClient() {
    if (!client) return;
    setSaving(true);
    const payload = {
      name: form.name,
      redirect_uris: form.redirectURIs.split(/\n+/).map((value) => value.trim()).filter(Boolean),
      scopes: form.scopes,
    };
    try {
      const response = await apiFetch(`/v1/oauth/clients/${client.client_id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!response.ok) throw new Error(await errorText(response, "Could not update OAuth client"));
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
    const response = await apiFetch(`/v1/oauth/clients/${client.client_id}`, { method: "DELETE" });
    if (!response.ok) {
      setError(await errorText(response, "Could not delete OAuth client"));
      return;
    }
    notify("OAuth client deleted");
    navigate("/settings");
  }

  return (
    <>
      <PageHeading
        eyebrow="OAuth client"
        title={client?.name ?? "OAuth client"}
        copy="Review this client registration, allowed scopes, and active user connections."
        actions={client && (
          <Group gap="xs">
            <Button variant="outline" onClick={openEdit}><SquarePen className="size-4" />Edit</Button>
            <Button variant="danger" onClick={deleteClient}><Trash2 className="size-4" />Delete</Button>
          </Group>
        )}
      />

      {error && <Alert color="red" mb="md">{error}</Alert>}

      {client && (
        <Stack gap="lg">
          <SimpleGrid cols={{ base: 1, md: 3 }} spacing="md">
            <Panel title="Client ID" eyebrow="Registration">
              <Group gap="xs" wrap="nowrap">
                <Code className="min-w-0 truncate">{client.client_id}</Code>
                <CopyButton label="Client ID" value={client.client_id} onCopy={copy} />
              </Group>
            </Panel>
            <Panel title="Status" eyebrow="Lifecycle">
              <Badge tone="green">Active</Badge>
              <Text c="dimmed" mt="xs" size="xs">Updated {new Date(client.updated_at).toLocaleString()}</Text>
            </Panel>
            <Panel title="Scopes" eyebrow={`${client.scopes.length} allowed`}>
              <ScopeBadges scopes={client.scopes} />
            </Panel>
          </SimpleGrid>

          <Panel title="Redirect URIs" eyebrow={`${client.redirect_uris.length} configured`}>
            <Stack gap="xs">
              {client.redirect_uris.map((uri) => (
                <Group key={uri} gap="xs" wrap="nowrap">
                  <Text c="dimmed" ff="monospace" size="xs" truncate>{uri}</Text>
                  <CopyButton label="Redirect URI" value={uri} onCopy={copy} />
                </Group>
              ))}
            </Stack>
          </Panel>

          <Box>
            <SectionHeading title="Connected" eyebrow={`${connections.length} active`} />
            <TableSurface>
              <ScrollArea>
                <Table highlightOnHover miw={860} verticalSpacing="sm" className="table-fixed">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th className="w-[30%]">User</Table.Th>
                      <Table.Th className="w-[30%]">Resource org</Table.Th>
                      <Table.Th className="w-[28%]">Granted scopes</Table.Th>
                      <Table.Th className="w-[12%]">Connected</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {connections.map((connection) => (
                      <Table.Tr key={`${connection.user_id}-${connection.org_id}`}>
                        <Table.Td className="w-[30%]"><Text fw={700} truncate>{connection.user_email}</Text><Text c="dimmed" ff="monospace" size="xs" truncate>{connection.user_id}</Text></Table.Td>
                        <Table.Td className="w-[30%]"><Text fw={700} truncate>{connection.org_name}</Text><Text c="dimmed" ff="monospace" size="xs" truncate>{connection.org_id}</Text></Table.Td>
                        <Table.Td className="w-[28%]"><ScopeBadges scopes={connection.scopes} /></Table.Td>
                        <Table.Td className="w-[12%]"><Text c="dimmed" size="sm" truncate>{new Date(connection.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Text></Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </ScrollArea>
              {!loading && !connections.length && <EmptyState icon={<PlugZap />} title="No active connections" copy="Approved users and resource organizations will appear here." />}
            </TableSurface>
          </Box>
        </Stack>
      )}

      {!loading && !client && !error && <EmptyState icon={<Settings />} title="Client not found" copy="This OAuth client is not owned by the active organization." />}

      <Modal opened={formOpen} onClose={() => setFormOpen(false)} title="Edit OAuth client" size="lg">
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
            <Button loading={saving} onClick={submitClient}><Check className="size-4" />Save changes</Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

function ScopeBadges({ scopes }: { scopes: string[] }) {
  return <Group gap={6}>{scopes.map((scope) => <Badge key={scope} tone="blue">{scope}</Badge>)}</Group>;
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
