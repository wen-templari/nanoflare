import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { ActionIcon, Alert, Box, Center, Code, Group, Modal, MultiSelect, ScrollArea, Select, Stack, Table, Text, TextInput, Textarea, Title, Tooltip } from "@mantine/core";
import { Ban, Check, Copy, Plus, RotateCcw, Settings, SquarePen, UserMinus, UserPlus } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { apiFetch, errorText, fetchJSON } from "../app/api";
import type { OAuthClient, OAuthClientCreated, OrganizationInvite, OrganizationInviteCreated, OrganizationMember } from "../app/types";
import { useWorkspace } from "../app/workspace-context";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { PageHeading } from "../components/shared/primitives";

const oauthScopes = ["apps:read", "apps:write", "deployments:write", "secrets:write", "kv:read", "kv:write", "objects:read", "objects:write"];
const roleOptions = ["viewer", "member", "admin", "owner"];
const emptyForm = { name: "", redirectURIs: "", scopes: [] as string[] };

export function SettingsPage() {
  const navigate = useNavigate();
  const { activeOrgID, organizations, notify } = useWorkspace();
  const [clients, setClients] = useState<OAuthClient[]>([]);
  const [members, setMembers] = useState<OrganizationMember[]>([]);
  const [invites, setInvites] = useState<OrganizationInvite[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [editingClient, setEditingClient] = useState<OAuthClient | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [oneTimeSecret, setOneTimeSecret] = useState<OAuthClientCreated | null>(null);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState("member");
  const [inviteCreated, setInviteCreated] = useState<OrganizationInviteCreated | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    void refresh();
  }, [activeOrgID]);

  const activeClients = useMemo(() => clients.filter((client) => !client.disabled), [clients]);
  const activeOrg = organizations.find((org) => org.id === activeOrgID);
  const canReadMembers = activeOrg?.scopes?.includes("members:read");
  const canWriteMembers = activeOrg?.scopes?.includes("members:write");
  const canManageOwners = activeOrg?.scopes?.includes("members:owner");
  const pendingInvites = useMemo(() => {
    const memberEmails = new Set(members.map((member) => member.user_email.toLowerCase()));
    return invites.filter((invite) => !invite.accepted_at && !invite.revoked_at && !memberEmails.has(invite.email.toLowerCase()));
  }, [invites, members]);

  async function refresh() {
    setLoading(true);
    try {
      const [nextClients, nextMembers, nextInvites] = await Promise.all([
        fetchJSON<OAuthClient[] | null>("/v1/oauth/clients").catch(() => []),
        canReadMembers ? fetchJSON<OrganizationMember[] | null>(`/v1/orgs/${activeOrgID}/members`).catch(() => []) : Promise.resolve([]),
        canReadMembers ? fetchJSON<OrganizationInvite[] | null>(`/v1/orgs/${activeOrgID}/invites`).catch(() => []) : Promise.resolve([]),
      ]);
      setClients(nextClients ?? []);
      setMembers(nextMembers ?? []);
      setInvites(nextInvites ?? []);
      setError("");
    } catch (err) {
      setClients([]);
      setMembers([]);
      setInvites([]);
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

  async function submitInvite() {
    const response = await apiFetch(`/v1/orgs/${activeOrgID}/invites`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: inviteEmail, role: inviteRole }),
    });
    if (!response.ok) {
      setError(await errorText(response, "Could not create invite"));
      return;
    }
    const invite = await response.json() as OrganizationInviteCreated;
    setInviteCreated(invite);
    setInviteOpen(false);
    setInviteEmail("");
    setInviteRole("member");
    notify("Invite created");
    await refresh();
  }

  async function updateMember(member: OrganizationMember, role: string) {
    const response = await apiFetch(`/v1/orgs/${activeOrgID}/members/${member.user_id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ role }),
    });
    if (!response.ok) {
      setError(await errorText(response, "Could not update member"));
      return;
    }
    notify("Member updated");
    await refresh();
  }

  async function removeMember(member: OrganizationMember) {
    const response = await apiFetch(`/v1/orgs/${activeOrgID}/members/${member.user_id}`, { method: "DELETE" });
    if (!response.ok) {
      setError(await errorText(response, "Could not remove member"));
      return;
    }
    notify("Member removed");
    await refresh();
  }

  async function revokeInvite(invite: OrganizationInvite) {
    const response = await apiFetch(`/v1/orgs/${activeOrgID}/invites/${invite.id}`, { method: "DELETE" });
    if (!response.ok) {
      setError(await errorText(response, "Could not remove invite"));
      return;
    }
    notify("Invite removed");
    await refresh();
  }

  return (
    <>
      <PageHeading
        eyebrow="Settings"
        title="Settings"
        copy="Register OAuth apps for this organization and review the external apps you have connected."
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

      {inviteCreated && (
        <Alert color="blue" mb="md" title="Invite link">
          <Group align="center" justify="space-between" wrap="nowrap">
            <Code className="block break-all">{inviteCreated.invite_url}</Code>
            <Button variant="outline" onClick={() => copy(inviteCreated.invite_url, "Invite link")}><Copy className="size-4" />Copy</Button>
          </Group>
        </Alert>
      )}

      <Stack gap="lg">
        {canReadMembers && (
          <Box>
            <SectionHeading
              title="Members"
              eyebrow={`${members.length + pendingInvites.length} users`}
              actions={canWriteMembers && <Button onClick={() => setInviteOpen(true)}><UserPlus className="size-4" />Invite</Button>}
            />
            <TableSurface>
              <ScrollArea>
                <Table highlightOnHover miw={720} verticalSpacing="sm" className="table-fixed">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th className="w-[42%]">User</Table.Th>
                      <Table.Th className="w-[24%]">Role</Table.Th>
                      <Table.Th className="w-[14%]">Status</Table.Th>
                      <Table.Th className="w-[10%]">Date</Table.Th>
                      <Table.Th className="w-[10%]">Actions</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {members.map((member) => {
                      const ownerChange = member.role === "owner";
                      const canEditMember = canWriteMembers && (!ownerChange || canManageOwners);
                      return (
                        <Table.Tr key={member.user_id}>
                          <Table.Td><Text fw={700} truncate>{member.user_email}</Text></Table.Td>
                          <Table.Td>
                            <Select
                              allowDeselect={false}
                              data={roleOptions}
                              disabled={!canEditMember}
                              onChange={(role) => role && updateMember(member, role)}
                              size="xs"
                              value={member.role}
                            />
                          </Table.Td>
                          <Table.Td><Badge tone="green">Joined</Badge></Table.Td>
                          <Table.Td><Text c="dimmed" size="sm">{new Date(member.created_at).toLocaleDateString()}</Text></Table.Td>
                          <Table.Td>
                            <Tooltip label="Remove member">
                              <ActionIcon aria-label="Remove member" color="red" disabled={!canEditMember} onClick={() => removeMember(member)} variant="subtle"><UserMinus size={16} /></ActionIcon>
                            </Tooltip>
                          </Table.Td>
                        </Table.Tr>
                      );
                    })}
                    {pendingInvites.map((invite) => (
                      <Table.Tr key={invite.id} opacity={0.74}>
                        <Table.Td><Text fw={700} truncate>{invite.email}</Text></Table.Td>
                        <Table.Td><Badge tone="blue">{invite.role}</Badge></Table.Td>
                        <Table.Td><Badge tone="orange">Pending</Badge></Table.Td>
                        <Table.Td><Text c="dimmed" size="sm">{new Date(invite.expires_at).toLocaleDateString()}</Text></Table.Td>
                        <Table.Td>
                          <Tooltip label="Remove invite">
                            <ActionIcon aria-label="Remove invite" color="red" disabled={!canWriteMembers} onClick={() => revokeInvite(invite)} variant="subtle"><UserMinus size={16} /></ActionIcon>
                          </Tooltip>
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </ScrollArea>
            </TableSurface>
          </Box>
        )}

        <Box>
          <SectionHeading
            title="OAuth clients"
            eyebrow={`${activeClients.length} active`}
            actions={<Button onClick={openCreate}><Plus className="size-4" />New OAuth client</Button>}
          />
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

      <Modal opened={inviteOpen} onClose={() => setInviteOpen(false)} title="Invite member">
        <Stack>
          <TextInput label="Email" type="email" value={inviteEmail} onChange={(event) => setInviteEmail(event.currentTarget.value)} />
          <Select allowDeselect={false} data={canManageOwners ? roleOptions : roleOptions.filter((role) => role !== "owner")} label="Role" value={inviteRole} onChange={(role) => role && setInviteRole(role)} />
          <Group justify="end">
            <Button variant="ghost" onClick={() => setInviteOpen(false)}>Cancel</Button>
            <Button onClick={submitInvite}><Check className="size-4" />Create invite</Button>
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

function SectionHeading({ title, eyebrow, actions }: { title: string; eyebrow: string; actions?: ReactNode }) {
  return (
    <Group align="end" justify="space-between" mb="sm">
      <Box>
        <Text c="dimmed" fw={700} size="xs" tt="uppercase">{eyebrow}</Text>
        <Title mt={2} order={3} size="h5">{title}</Title>
      </Box>
      {actions}
    </Group>
  );
}

function TableSurface({ children }: { children: ReactNode }) {
  return <Box bg="white" className="overflow-hidden rounded-lg border border-[var(--mantine-color-gray-3)]">{children}</Box>;
}

function EmptyState({ icon, title, copy }: { icon: ReactNode; title: string; copy: string }) {
  return <Center h={220}><Stack align="center" gap={4} ta="center" className="[&_svg]:size-6">{icon}<Text fw={700} size="sm">{title}</Text><Text c="dimmed" size="xs">{copy}</Text></Stack></Center>;
}
