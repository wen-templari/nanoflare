import type { ChangeEvent, MouseEvent, ReactNode } from "react";

import {
  Banner,
  Button,
  cn,
  Dialog,
  Label,
  Meter,
  Select as KumoSelect,
  Table as KumoTable,
  Text as KumoText,
  Textarea as KumoTextarea,
  Tooltip as KumoTooltip,
} from "@cloudflare/kumo";
import {
  Check,
  Copy,
  Plus,
  RotateCcw,
  Settings,
  SquarePen,
  Trash2,
  UserMinus,
  UserPlus,
  X,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";

import type {
  OAuthClient,
  OAuthClientCreated,
  OrganizationInvite,
  OrganizationInviteCreated,
  OrganizationMember,
  PersonalAccessToken,
  PersonalAccessTokenCreated,
} from "../app/types";

import { apiClient, errorMessage } from "../app/api";
import {
  formatBytes,
  normalizeUsageLevel,
  orgLimitsForLevel,
  usageLevelPaid,
} from "../app/org-limits";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading, Panel } from "../components/shared/primitives";
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
const controlScopes = [
  "workers:read",
  "workers:write",
  "deployments:write",
  "secrets:write",
  "kv:read",
  "kv:write",
  "db:read",
  "db:write",
  "objects:read",
  "objects:write",
  "orgs:read",
  "orgs:write",
  "members:read",
  "members:write",
  "members:owner",
];
const roleOptions = ["viewer", "member", "admin", "owner"];
const emptyForm = { name: "", redirectURIs: "", scopes: [] as string[] };
const emptyPATForm = { name: "", scopeType: "org", scopes: [] as string[], expiresIn: "never" };

function Alert({
  children,
  color,
  title,
  ...props
}: {
  children: ReactNode;
  color?: string;
  title?: string;
  [key: string]: unknown;
}) {
  return (
    <Banner {...(props as any)} variant={color === "red" ? "error" : "default"}>
      {title && (
        <Text as="h2" variant="heading3" size="sm">
          {title}
        </Text>
      )}
      {children}
    </Banner>
  );
}

function Text({ children, ...props }: { children: ReactNode; [key: string]: unknown }) {
  return <KumoText {...(props as any)}>{children}</KumoText>;
}

function Tooltip({
  children,
  label,
  ...props
}: {
  children: ReactNode;
  label: ReactNode;
  [key: string]: unknown;
}) {
  return (
    <KumoTooltip {...(props as any)} content={label}>
      {children}
    </KumoTooltip>
  );
}

function Box({ children, ...props }: { children: ReactNode; [key: string]: unknown }) {
  return <div {...(props as any)}>{children}</div>;
}

function Group({ children, ...props }: { children: ReactNode; [key: string]: unknown }) {
  return (
    <div {...(props as any)} className="flex items-center gap-2">
      {children}
    </div>
  );
}

function Stack({ children, ...props }: { children: ReactNode; [key: string]: unknown }) {
  return (
    <div {...(props as any)} className="flex flex-col gap-4">
      {children}
    </div>
  );
}

function Code({
  children,
  className,
  ...props
}: {
  children: ReactNode;
  className?: string;
  [key: string]: unknown;
}) {
  return (
    <code {...(props as any)} className={cn("font-mono text-[0.9em]", className)}>
      {children}
    </code>
  );
}

function Modal({
  opened,
  onClose,
  title,
  children,
  size,
}: {
  opened: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  size?: "lg" | "xl";
}) {
  return (
    <Dialog.Root open={opened} onOpenChange={(open) => !open && onClose()}>
      <Dialog size={size} className="p-6">
        <div className="mb-5 flex items-start justify-between gap-4">
          <Dialog.Title className="text-lg font-semibold">{title}</Dialog.Title>
          <Dialog.Close
            render={(props) => (
              <Button {...props} aria-label="Close" shape="square" size="sm" variant="ghost">
                <X className="size-4" />
              </Button>
            )}
          />
        </div>
        {children}
      </Dialog>
    </Dialog.Root>
  );
}

function MultiSelect({
  data,
  label,
  value,
  onChange,
}: {
  data: string[];
  label: string;
  value: string[];
  onChange: (value: string[]) => void;
}) {
  return (
    <fieldset className="grid gap-2">
      <legend>
        <Label asContent>{label}</Label>
      </legend>
      <div className="flex flex-wrap gap-x-4 gap-y-2">
        {data.map((item) => (
          <label className="flex items-center gap-2 text-sm" key={item}>
            <input
              checked={value.includes(item)}
              onChange={(event) =>
                onChange(
                  event.target.checked ? [...value, item] : value.filter((scope) => scope !== item),
                )
              }
              type="checkbox"
            />
            {item}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

function Progress({
  value,
  color,
  ...props
}: {
  value: number;
  color?: string;
  [key: string]: unknown;
}) {
  return (
    <Meter
      {...(props as any)}
      aria-label="Usage"
      className="mt-1"
      indicatorClassName={color === "orange" ? "bg-kumo-warning" : undefined}
      label=""
      showValue={false}
      value={value}
    />
  );
}

function ScrollArea({ children }: { children: ReactNode }) {
  return <div className="overflow-x-auto">{children}</div>;
}

function Select({
  data,
  onChange,
  ...props
}: {
  data: (string | { value: string; label: string })[];
  onChange?: (value: string | null) => void;
  [key: string]: unknown;
}) {
  return (
    <KumoSelect
      {...(props as any)}
      onValueChange={(value) => onChange?.(typeof value === "string" ? value : null)}
    >
      {data.map((item) => {
        const option = typeof item === "string" ? { value: item, label: item } : item;
        return (
          <KumoSelect.Option key={option.value} value={option.value}>
            {option.label}
          </KumoSelect.Option>
        );
      })}
    </KumoSelect>
  );
}

const Table: any = Object.assign(KumoTable, {
  Thead: KumoTable.Header,
  Tbody: KumoTable.Body,
  Tr: KumoTable.Row,
  Th: KumoTable.Head,
  Td: KumoTable.Cell,
});
const TextInput = Input;
const Textarea: any = ({
  autosize,
  minRows,
  ...props
}: {
  autosize?: boolean;
  minRows?: number;
  [key: string]: unknown;
}) => <KumoTextarea {...(props as any)} autoResize={autosize} minRows={minRows} />;
const Title = ({ children, ...props }: { children: ReactNode; [key: string]: unknown }) => (
  <Text {...(props as any)} as="h3" variant="heading3">
    {children}
  </Text>
);

export function SettingsPage() {
  const navigate = useNavigate();
  const { activeOrgID, organizations, workers, namespaces, objectStorageBuckets, notify } =
    useWorkspace();
  const [clients, setClients] = useState<OAuthClient[]>([]);
  const [pats, setPats] = useState<PersonalAccessToken[]>([]);
  const [members, setMembers] = useState<OrganizationMember[]>([]);
  const [invites, setInvites] = useState<OrganizationInvite[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [editingClient, setEditingClient] = useState<OAuthClient | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [oneTimeSecret, setOneTimeSecret] = useState<OAuthClientCreated | null>(null);
  const [oneTimePAT, setOneTimePAT] = useState<PersonalAccessTokenCreated | null>(null);
  const [selectedPAT, setSelectedPAT] = useState<PersonalAccessToken | null>(null);
  const [patOpen, setPATOpen] = useState(false);
  const [patForm, setPATForm] = useState(emptyPATForm);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState("member");
  const [inviteCreated, setInviteCreated] = useState<OrganizationInviteCreated | null>(null);
  const [error, setError] = useState("");
  const [quotaUsage, setQuotaUsage] = useState({ kvBytes: 0, objectBytes: 0, loading: true });

  useEffect(() => {
    void refresh();
  }, [activeOrgID]);

  const activeOrg = organizations.find((org) => org.id === activeOrgID);
  const usageLevel = normalizeUsageLevel(activeOrg?.usage_level);
  const limits = useMemo(() => orgLimitsForLevel(usageLevel), [usageLevel]);
  const canCreateOAuthClient = limits.oauthClients === null || limits.oauthClients > 0;
  const namespaceIDs = useMemo(
    () =>
      namespaces
        .map((namespace) => namespace.id)
        .sort()
        .join(","),
    [namespaces],
  );
  const bucketIDs = useMemo(
    () =>
      objectStorageBuckets
        .map((bucket) => bucket.id)
        .sort()
        .join(","),
    [objectStorageBuckets],
  );
  const canReadMembers = activeOrg?.scopes?.includes("members:read");
  const canWriteMembers = activeOrg?.scopes?.includes("members:write");
  const canManageOwners = activeOrg?.scopes?.includes("members:owner");
  const patScopeOptions =
    patForm.scopeType === "org"
      ? controlScopes.filter((scope) => activeOrg?.scopes?.includes(scope))
      : controlScopes;
  const pendingInvites = useMemo(() => {
    const memberEmails = new Set(
      members.flatMap((member) => (member.user_email ? [member.user_email.toLowerCase()] : [])),
    );
    return invites.filter(
      (invite) =>
        !invite.accepted_at && !invite.revoked_at && !memberEmails.has(invite.email.toLowerCase()),
    );
  }, [invites, members]);

  useEffect(() => {
    let cancelled = false;

    async function loadQuotaUsage() {
      if (!activeOrgID) {
        setQuotaUsage({ kvBytes: 0, objectBytes: 0, loading: false });
        return;
      }

      setQuotaUsage((current) => ({ ...current, loading: true }));
      const [kvMetrics, bucketMetrics] = await Promise.all([
        Promise.all(
          namespaces.map((namespace) =>
            apiClient
              .GET("/v1/organizations/{orgID}/kv-namespaces/{namespaceID}/analytics", {
                params: { path: { orgID: activeOrgID, namespaceID: namespace.id } },
              })
              .then(({ data }) => data ?? { available: false, reads: 0, writes: 0, size: 0 }),
          ),
        ),
        Promise.all(
          objectStorageBuckets.map((bucket) =>
            apiClient
              .GET("/v1/organizations/{orgID}/object-storage-buckets/{bucketID}/analytics", {
                params: { path: { orgID: activeOrgID, bucketID: bucket.id } },
              })
              .then(({ data }) => data ?? { available: false, reads: 0, writes: 0, size: 0 }),
          ),
        ),
      ]);

      if (cancelled) return;
      setQuotaUsage({
        kvBytes: kvMetrics.reduce((sum, metrics) => sum + (metrics.size ?? 0), 0),
        objectBytes: bucketMetrics.reduce((sum, metrics) => sum + (metrics.size ?? 0), 0),
        loading: false,
      });
    }

    void loadQuotaUsage();
    const interval = window.setInterval(() => void loadQuotaUsage(), 15000);

    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [activeOrgID, bucketIDs, namespaceIDs, namespaces, objectStorageBuckets]);

  async function refresh() {
    setLoading(true);
    try {
      const [nextClients, nextPATs, nextMembers, nextInvites] = await Promise.all([
        apiClient
          .GET("/v1/organizations/{orgID}/oauth-clients", {
            params: { path: { orgID: activeOrgID } },
            parseAs: "json",
          })
          .then(({ data }) => data ?? []),
        apiClient
          .GET("/v1/me/personal-access-tokens", { parseAs: "json" })
          .then(({ data }) => data ?? []),
        canReadMembers
          ? apiClient
              .GET("/v1/organizations/{orgID}/members", {
                params: { path: { orgID: activeOrgID } },
              })
              .then(({ data }) => data ?? [])
          : Promise.resolve([]),
        canReadMembers
          ? apiClient
              .GET("/v1/organizations/{orgID}/invites", {
                params: { path: { orgID: activeOrgID } },
              })
              .then(({ data }) => data ?? [])
          : Promise.resolve([]),
      ]);
      setClients(nextClients.filter((client) => !client.disabled));
      setPats(nextPATs.filter((token) => !token.revoked_at));
      setMembers(nextMembers);
      setInvites(nextInvites);
      setError("");
    } catch (err) {
      setClients([]);
      setPats([]);
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
    setForm({
      name: client.name,
      redirectURIs: (client.redirect_uris ?? []).join("\n"),
      scopes: client.scopes ?? [],
    });
    setError("");
    setFormOpen(true);
  }

  async function submitClient() {
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
      if (editingClient) {
        const { data, error } = await apiClient.PATCH(
          "/v1/organizations/{orgID}/oauth-clients/{clientID}",
          {
            params: { path: { orgID: activeOrgID, clientID: editingClient.client_id } },
            body: payload,
            parseAs: "json",
          },
        );
        if (error || !data) throw new Error(errorMessage(error, "Could not save OAuth client"));
      } else {
        const { data, error } = await apiClient.POST("/v1/organizations/{orgID}/oauth-clients", {
          params: { path: { orgID: activeOrgID } },
          body: payload,
          parseAs: "json",
        });
        if (error || !data) throw new Error(errorMessage(error, "Could not save OAuth client"));
        setOneTimeSecret(data);
      }
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
    const { data, error } = await apiClient.POST(
      "/v1/organizations/{orgID}/oauth-clients/{clientID}/client-secrets",
      { params: { path: { orgID: activeOrgID, clientID: client.client_id } }, parseAs: "json" },
    );
    if (error || !data) {
      setError(errorMessage(error, "Could not rotate client secret"));
      return;
    }
    setOneTimeSecret(data);
    notify("OAuth client secret rotated");
    await refresh();
  }

  async function deleteClient(client: OAuthClient) {
    const { error } = await apiClient.DELETE("/v1/organizations/{orgID}/oauth-clients/{clientID}", {
      params: { path: { orgID: activeOrgID, clientID: client.client_id } },
      parseAs: "json",
    });
    if (error) {
      setError(errorMessage(error, "Could not delete OAuth client"));
      return;
    }
    notify("OAuth client deleted");
    await refresh();
  }

  async function copy(value: string, label: string) {
    await navigator.clipboard.writeText(value);
    notify(`${label} copied`);
  }

  function openCreatePAT() {
    setPATForm({
      ...emptyPATForm,
      scopes: activeOrg?.scopes?.filter((scope) => controlScopes.includes(scope)) ?? [],
    });
    setError("");
    setPATOpen(true);
  }

  async function submitPAT() {
    setSaving(true);
    const expiresAt = expiryDate(patForm.expiresIn);
    const payload = {
      name: patForm.name,
      scope_type: patForm.scopeType,
      org_id: patForm.scopeType === "org" ? activeOrgID : undefined,
      scopes: patForm.scopes,
      expires_at: expiresAt,
    };
    try {
      const { data: created, error } = await apiClient.POST("/v1/me/personal-access-tokens", {
        body: payload,
        parseAs: "json",
      });
      if (error || !created)
        throw new Error(errorMessage(error, "Could not create personal access token"));
      setOneTimePAT(created);
      setPats((current) => [created, ...current.filter((token) => token.id !== created.id)]);
      setPATOpen(false);
      setPATForm(emptyPATForm);
      notify("Personal access token created");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create personal access token");
    } finally {
      setSaving(false);
    }
  }

  async function revokePAT(token: PersonalAccessToken) {
    const { error } = await apiClient.DELETE("/v1/me/personal-access-tokens/{patID}", {
      params: { path: { patID: token.id } },
      parseAs: "json",
    });
    if (error) {
      setError(errorMessage(error, "Could not revoke personal access token"));
      return;
    }
    notify("Personal access token revoked");
    await refresh();
  }

  async function submitInvite() {
    const { data: invite, error } = await apiClient.POST("/v1/organizations/{orgID}/invites", {
      params: { path: { orgID: activeOrgID } },
      body: { email: inviteEmail, role: inviteRole },
      parseAs: "json",
    });
    if (error || !invite) {
      setError(errorMessage(error, "Could not create invite"));
      return;
    }
    setInviteCreated(invite);
    setInviteOpen(false);
    setInviteEmail("");
    setInviteRole("member");
    notify("Invite created");
    await refresh();
  }

  async function updateMember(member: OrganizationMember, role: string) {
    const { error } = await apiClient.PATCH("/v1/organizations/{orgID}/members/{userID}", {
      params: { path: { orgID: activeOrgID, userID: member.user_id } },
      body: { role },
      parseAs: "json",
    });
    if (error) {
      setError(errorMessage(error, "Could not update member"));
      return;
    }
    notify("Member updated");
    await refresh();
  }

  async function removeMember(member: OrganizationMember) {
    const { error } = await apiClient.DELETE("/v1/organizations/{orgID}/members/{userID}", {
      params: { path: { orgID: activeOrgID, userID: member.user_id } },
      parseAs: "json",
    });
    if (error) {
      setError(errorMessage(error, "Could not remove member"));
      return;
    }
    notify("Member removed");
    await refresh();
  }

  async function revokeInvite(invite: OrganizationInvite) {
    const { error } = await apiClient.DELETE("/v1/organizations/{orgID}/invites/{inviteID}", {
      params: { path: { orgID: activeOrgID, inviteID: invite.id } },
      parseAs: "json",
    });
    if (error) {
      setError(errorMessage(error, "Could not remove invite"));
      return;
    }
    notify("Invite removed");
    await refresh();
  }

  return (
    <>
      <PageHeading eyebrow="Settings" title="Settings" />

      {error && (
        <Alert color="red" mb="md">
          {error}
        </Alert>
      )}

      {oneTimeSecret && (
        <Alert color="blue" mb="md" title="Client secret shown once">
          <Group align="center" justify="space-between" wrap="nowrap">
            <Box>
              <Text size="sm">
                Store this secret now. Nanoflare will not show it again after this page refreshes.
              </Text>
              <Code mt={8} className="block break-all">
                {oneTimeSecret.client_secret}
              </Code>
            </Box>
            <Button
              variant="outline"
              onClick={() => copy(oneTimeSecret.client_secret, "Client secret")}
            >
              <Copy className="size-4" />
              Copy
            </Button>
          </Group>
        </Alert>
      )}

      <div className="flex flex-col gap-8">
        <Panel title="Usage" eyebrow={usageLevel === usageLevelPaid ? "Paid plan" : "Default plan"}>
          <div className="grid gap-3 sm:grid-cols-2">
            <LimitRow current={workers.length} label="Workers" limit={limits.workers} />
            <LimitRow
              current={namespaces.length}
              label="KV namespaces"
              limit={limits.kvNamespaces}
            />
            <LimitRow
              current={quotaUsage.kvBytes}
              format={formatBytes}
              label="KV storage"
              limit={limits.kvStorageBytes}
              loading={quotaUsage.loading}
            />
            <LimitRow
              current={objectStorageBuckets.length}
              label="Object buckets"
              limit={limits.objectStorageBuckets}
            />
            <LimitRow
              current={quotaUsage.objectBytes}
              format={formatBytes}
              label="Object storage"
              limit={limits.objectStorageBytes}
              loading={quotaUsage.loading}
            />
            <LimitRow
              current={clients.length}
              label="OAuth clients"
              limit={limits.oauthClients}
              loading={loading}
            />
          </div>
        </Panel>

        <Box>
          <SectionHeading
            title="Personal access tokens"
            actions={
              <Button onClick={openCreatePAT}>
                <Plus className="size-4" />
                Create token
              </Button>
            }
          />
          <TableSurface>
            <ScrollArea>
              <Table highlightOnHover miw={960} verticalSpacing="sm" className="table-fixed">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th className="w-[22%]">Name</Table.Th>
                    <Table.Th className="w-[16%]">Scope</Table.Th>
                    <Table.Th className="w-[18%]">Owner</Table.Th>
                    <Table.Th className="w-[15%]">Created</Table.Th>
                    <Table.Th className="w-[13%]">Expires</Table.Th>
                    <Table.Th className="w-[10%]">Last used</Table.Th>
                    <Table.Th className="w-[6%] text-center">Actions</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {pats.map((token) => (
                    <Table.Tr
                      className="cursor-pointer"
                      key={token.id}
                      onClick={() => setSelectedPAT(token)}
                    >
                      <Table.Td>
                        <Text fw={700} truncate>
                          {token.name}
                        </Text>
                      </Table.Td>
                      <Table.Td className="w-[16%]">
                        <div className="flex items-center gap-2">
                          <Badge tone={token.scope_type === "org" ? "blue" : "green"}>
                            {token.scope_type === "org" ? "Organization" : "User"}
                          </Badge>
                          <Text c="dimmed" size="sm">
                            {(token.scopes ?? []).length}
                          </Text>
                        </div>
                      </Table.Td>
                      <Table.Td className="w-[18%]">
                        <Text c="dimmed" size="sm" truncate>
                          {token.scope_type === "org"
                            ? orgName(token.org_id, organizations)
                            : "All organizations"}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text c="dimmed" size="sm">
                          {formatDate(token.created_at)}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text c="dimmed" size="sm">
                          {formatDate(token.expires_at)}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text c="dimmed" size="sm">
                          {formatDate(token.last_used_at)}
                        </Text>
                      </Table.Td>
                      <Table.Td className="text-center">
                        <Tooltip label="Revoke token">
                          <Button
                            aria-label="Revoke token"
                            onClick={(event: MouseEvent<HTMLButtonElement>) => {
                              event.stopPropagation();
                              void revokePAT(token);
                            }}
                            shape="square"
                            variant="destructive"
                          >
                            <Trash2 size={16} />
                          </Button>
                        </Tooltip>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </ScrollArea>
            {!loading && !pats.length && (
              <EmptyState
                icon={<Settings />}
                title="No personal access tokens"
                copy="Create one to authenticate automation or the Nanoflare CLI."
              />
            )}
          </TableSurface>
        </Box>

        {canReadMembers && (
          <Box>
            <SectionHeading
              title="Members"
              actions={
                canWriteMembers && (
                  <Button onClick={() => setInviteOpen(true)}>
                    <UserPlus className="size-4" />
                    Invite
                  </Button>
                )
              }
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
                      <Table.Th className="w-[10%] text-center">Actions</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {members.map((member) => {
                      const ownerChange = member.role === "owner";
                      const canEditMember = canWriteMembers && (!ownerChange || canManageOwners);
                      return (
                        <Table.Tr key={member.user_id}>
                          <Table.Td>
                            <Text fw={700} truncate>
                              {member.user_email}
                            </Text>
                          </Table.Td>
                          <Table.Td>
                            <KumoSelect
                              aria-label={`Role for ${member.user_email}`}
                              disabled={!canEditMember}
                              items={roleOptions.map((role) => ({ label: role, value: role }))}
                              onValueChange={(role) => {
                                if (typeof role === "string") void updateMember(member, role);
                              }}
                              size="sm"
                              value={member.role}
                            />
                          </Table.Td>
                          <Table.Td>
                            <Badge tone="green">Joined</Badge>
                          </Table.Td>
                          <Table.Td>
                            <Text c="dimmed" size="sm">
                              {new Date(member.created_at).toLocaleDateString()}
                            </Text>
                          </Table.Td>
                          <Table.Td className="text-center">
                            <Tooltip label="Remove member">
                              <Button
                                aria-label="Remove member"
                                disabled={!canEditMember}
                                onClick={() => removeMember(member)}
                                shape="square"
                                variant="destructive"
                              >
                                <UserMinus size={16} />
                              </Button>
                            </Tooltip>
                          </Table.Td>
                        </Table.Tr>
                      );
                    })}
                    {pendingInvites.map((invite) => (
                      <Table.Tr key={invite.id} opacity={0.74}>
                        <Table.Td>
                          <Text fw={700} truncate>
                            {invite.email}
                          </Text>
                        </Table.Td>
                        <Table.Td>
                          <Badge tone="blue">{invite.role}</Badge>
                        </Table.Td>
                        <Table.Td>
                          <Badge tone="orange">Pending</Badge>
                        </Table.Td>
                        <Table.Td>
                          <Text c="dimmed" size="sm">
                            {new Date(invite.expires_at).toLocaleDateString()}
                          </Text>
                        </Table.Td>
                        <Table.Td className="text-center">
                          <Tooltip label="Remove invite">
                            <Button
                              aria-label="Remove invite"
                              disabled={!canWriteMembers}
                              onClick={() => revokeInvite(invite)}
                              shape="square"
                              variant="destructive"
                            >
                              <UserMinus size={16} />
                            </Button>
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
            actions={
              canCreateOAuthClient ? (
                <Button onClick={openCreate}>
                  <Plus className="size-4" />
                  Create OAuth client
                </Button>
              ) : (
                <Text c="dimmed" size="sm">
                  Default plan does not include OAuth clients.
                </Text>
              )
            }
          />
          {canCreateOAuthClient ? (
            <TableSurface>
              <ScrollArea>
                <Table highlightOnHover miw={900} verticalSpacing="sm" className="table-fixed">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th className="w-[28%]">Client</Table.Th>
                      <Table.Th className="w-[34%]">Client ID</Table.Th>
                      <Table.Th className="w-[18%]">Redirect URIs</Table.Th>
                      <Table.Th className="w-[10%]">Updated</Table.Th>
                      <Table.Th className="w-[10%] text-center">Actions</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {clients.map((client) => (
                      <Table.Tr
                        key={client.client_id}
                        className="cursor-pointer"
                        onClick={() => navigate(`/settings/oauth-clients/${client.client_id}`)}
                      >
                        <Table.Td className="w-[28%]">
                          <Group gap="sm" wrap="nowrap">
                            <Text fw={700} truncate>
                              {client.name}
                            </Text>
                            <Badge tone="green">Active</Badge>
                          </Group>
                        </Table.Td>
                        <Table.Td className="w-[34%]">
                          <Group gap="xs" wrap="nowrap">
                            <Text c="dimmed" ff="monospace" size="xs" truncate>
                              {client.client_id}
                            </Text>
                            <CopyButton label="Client ID" value={client.client_id} onCopy={copy} />
                          </Group>
                        </Table.Td>
                        <Table.Td className="w-[18%]">
                          <Stack gap={4}>
                            {(client.redirect_uris ?? []).map((uri) => (
                              <Text c="dimmed" ff="monospace" key={uri} size="xs" truncate>
                                {uri}
                              </Text>
                            ))}
                          </Stack>
                        </Table.Td>
                        <Table.Td className="w-[10%]">
                          <Text c="dimmed" size="sm" truncate>
                            {new Date(client.updated_at).toLocaleDateString(undefined, {
                              month: "short",
                              day: "numeric",
                            })}
                          </Text>
                        </Table.Td>
                        <Table.Td className="w-[10%]">
                          <div className="flex items-center justify-center gap-1">
                            <Tooltip label="Edit client">
                              <Button
                                aria-label="Edit client"
                                onClick={(event: MouseEvent<HTMLButtonElement>) => {
                                  event.stopPropagation();
                                  openEdit(client);
                                }}
                                shape="square"
                                variant="ghost"
                              >
                                <SquarePen size={16} />
                              </Button>
                            </Tooltip>
                            <Tooltip label="Rotate secret">
                              <Button
                                aria-label="Rotate secret"
                                onClick={(event: MouseEvent<HTMLButtonElement>) => {
                                  event.stopPropagation();
                                  void rotateSecret(client);
                                }}
                                shape="square"
                                variant="ghost"
                              >
                                <RotateCcw size={16} />
                              </Button>
                            </Tooltip>
                            <Tooltip label="Delete client">
                              <Button
                                aria-label="Delete client"
                                onClick={(event: MouseEvent<HTMLButtonElement>) => {
                                  event.stopPropagation();
                                  void deleteClient(client);
                                }}
                                shape="square"
                                variant="destructive"
                              >
                                <Trash2 size={16} />
                              </Button>
                            </Tooltip>
                          </div>
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </ScrollArea>
              {!loading && !clients.length && (
                <EmptyState
                  icon={<Settings />}
                  title="No OAuth clients"
                  copy="Create one to let an external platform connect to Nanoflare."
                />
              )}
            </TableSurface>
          ) : (
            <EmptyState
              icon={<Settings />}
              title="OAuth clients unavailable"
              copy="OAuth clients are available on the paid plan."
            />
          )}
        </Box>
      </div>

      <Modal
        opened={formOpen}
        onClose={() => setFormOpen(false)}
        title={editingClient ? "Edit OAuth client" : "Create OAuth client"}
        size="lg"
      >
        <Stack>
          <TextInput
            label="Name"
            value={form.name}
            onChange={(event) => {
              const name = event.currentTarget.value;
              setForm((current) => ({ ...current, name }));
            }}
          />
          <Textarea
            autosize
            label="Redirect URIs"
            minRows={3}
            value={form.redirectURIs}
            onChange={(event: ChangeEvent<HTMLTextAreaElement>) => {
              const redirectURIs = event.currentTarget.value;
              setForm((current) => ({ ...current, redirectURIs }));
            }}
          />
          <MultiSelect
            data={oauthScopes}
            label="Allowed scopes"
            value={form.scopes}
            onChange={(scopes) => setForm((current) => ({ ...current, scopes }))}
          />
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setFormOpen(false)}>
              Cancel
            </Button>
            <Button loading={saving} onClick={submitClient}>
              <Check className="size-4" />
              {editingClient ? "Save changes" : "Create client"}
            </Button>
          </div>
        </Stack>
      </Modal>

      <Modal
        opened={patOpen}
        onClose={() => setPATOpen(false)}
        title="Create personal access token"
        size="lg"
      >
        <Stack>
          <TextInput
            label="Name"
            value={patForm.name}
            onChange={(event) => {
              const name = event.currentTarget.value;
              setPATForm((current) => ({ ...current, name }));
            }}
          />
          <Select
            allowDeselect={false}
            data={[
              { value: "org", label: "Current organization" },
              { value: "user", label: "User scoped" },
            ]}
            label="Scope"
            value={patForm.scopeType}
            onChange={(scopeType) =>
              scopeType &&
              setPATForm((current) => ({
                ...current,
                scopeType,
                scopes:
                  scopeType === "org"
                    ? (activeOrg?.scopes?.filter((scope) => controlScopes.includes(scope)) ?? [])
                    : controlScopes,
              }))
            }
          />
          <MultiSelect
            data={patScopeOptions}
            label="Allowed scopes"
            value={patForm.scopes}
            onChange={(scopes) => setPATForm((current) => ({ ...current, scopes }))}
          />
          <Select
            allowDeselect={false}
            data={[
              { value: "never", label: "Never" },
              { value: "30", label: "30 days" },
              { value: "90", label: "90 days" },
              { value: "365", label: "1 year" },
            ]}
            label="Expiration"
            value={patForm.expiresIn}
            onChange={(expiresIn) =>
              expiresIn && setPATForm((current) => ({ ...current, expiresIn }))
            }
          />
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setPATOpen(false)}>
              Cancel
            </Button>
            <Button loading={saving} onClick={submitPAT}>
              <Check className="size-4" />
              Create token
            </Button>
          </div>
        </Stack>
      </Modal>

      <Modal
        opened={Boolean(oneTimePAT)}
        onClose={() => setOneTimePAT(null)}
        title="Personal access token created"
        size="lg"
      >
        {oneTimePAT && (
          <div className="grid gap-5">
            <Banner variant="alert">
              Copy this token now. For security, it will not be shown again.
            </Banner>
            <div className="grid gap-2">
              <Label>Personal access token</Label>
              <Code className="block max-w-full rounded-lg bg-kumo-tint px-4 py-3 break-all">
                {oneTimePAT.access_token}
              </Code>
            </div>
            <div className="flex justify-end gap-2">
              <Button onClick={() => copy(oneTimePAT.access_token, "Personal access token")}>
                <Copy className="size-4" />
                Copy token
              </Button>
              <Button onClick={() => setOneTimePAT(null)} variant="secondary">
                Done
              </Button>
            </div>
          </div>
        )}
      </Modal>

      <Modal
        opened={Boolean(inviteCreated)}
        onClose={() => setInviteCreated(null)}
        title="Invite created"
        size="lg"
      >
        {inviteCreated && (
          <div className="grid gap-5">
            <Banner variant="alert">
              <p className="wrap-break-word text-sm">
                Share this link with <span className="font-medium">{inviteCreated.email}</span>. It
                is the only way to accept this invite.
              </p>
            </Banner>
            <div className="grid gap-2">
              <Label>Invite link</Label>
              <Code className="block max-w-full rounded-lg bg-kumo-tint px-4 py-3 break-all">
                {inviteCreated.invite_url}
              </Code>
            </div>
            <div className="flex justify-end gap-2">
              <Button onClick={() => copy(inviteCreated.invite_url, "Invite link")}>
                <Copy className="size-4" />
                Copy invite link
              </Button>
              <Button onClick={() => setInviteCreated(null)} variant="secondary">
                Done
              </Button>
            </div>
          </div>
        )}
      </Modal>

      <Modal
        opened={Boolean(selectedPAT)}
        onClose={() => setSelectedPAT(null)}
        title={selectedPAT ? `${selectedPAT.name} scopes` : "Personal access token scopes"}
        size="lg"
      >
        {selectedPAT && (
          <div className="grid gap-5">
            <div className="grid gap-1.5">
              <Label>Scope owner</Label>
              <Text size="sm" variant="secondary">
                {selectedPAT.scope_type === "org"
                  ? orgName(selectedPAT.org_id, organizations)
                  : "All organizations"}
              </Text>
            </div>
            <div className="grid gap-2">
              <Label>Allowed scopes</Label>
              <div className="flex flex-wrap gap-1.5">
                {(selectedPAT.scopes ?? []).map((scope) => (
                  <Badge key={scope} tone="blue">
                    {scope}
                  </Badge>
                ))}
              </div>
            </div>
          </div>
        )}
      </Modal>

      <Modal opened={inviteOpen} onClose={() => setInviteOpen(false)} title="Invite member">
        <Stack>
          <TextInput
            label="Email"
            type="email"
            value={inviteEmail}
            onChange={(event) => setInviteEmail(event.currentTarget.value)}
          />
          <Select
            allowDeselect={false}
            data={canManageOwners ? roleOptions : roleOptions.filter((role) => role !== "owner")}
            label="Role"
            value={inviteRole}
            onChange={(role) => role && setInviteRole(role)}
          />
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setInviteOpen(false)}>
              Cancel
            </Button>
            <Button onClick={submitInvite}>
              <Check className="size-4" />
              Create invite
            </Button>
          </div>
        </Stack>
      </Modal>
    </>
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
    <Tooltip label={`Copy ${label.toLowerCase()}`}>
      <Button
        aria-label={`Copy ${label.toLowerCase()}`}
        onClick={() => onCopy(value, label)}
        shape="square"
        size="sm"
        variant="ghost"
      >
        <Copy size={14} />
      </Button>
    </Tooltip>
  );
}

function LimitRow({
  current,
  format = formatCount,
  label,
  limit,
  loading = false,
}: {
  current: number;
  format?: (value: number) => string;
  label: string;
  limit: number | null;
  loading?: boolean;
}) {
  const hasLimit = limit !== null;
  const percent =
    hasLimit && limit > 0 ? Math.min((current / limit) * 100, 100) : current > 0 ? 100 : 0;
  const usageLabel = loading
    ? "Loading"
    : hasLimit
      ? `${format(current)} / ${format(limit)}`
      : `${format(current)} used`;

  return (
    <Box className="rounded-lg bg-kumo-tint px-4 py-3">
      <Group justify="space-between" mb={6}>
        <Text fw={700} size="sm">
          {label}
        </Text>
        <Text
          c={hasLimit ? (current >= limit ? "orange" : "dimmed") : "gray.7"}
          ff="monospace"
          size="xs"
        >
          {hasLimit ? usageLabel : `${usageLabel} · Unlimited`}
        </Text>
      </Group>
      {hasLimit && (
        <Progress
          color={current >= limit ? "orange" : "blue"}
          radius="xs"
          size="sm"
          value={percent}
        />
      )}
    </Box>
  );
}

function formatCount(value = 0) {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(value);
}

function expiryDate(value: string) {
  if (value === "never") return undefined;
  const days = Number(value);
  if (!Number.isFinite(days) || days <= 0) return undefined;
  const date = new Date();
  date.setDate(date.getDate() + days);
  return date.toISOString();
}

function formatDate(value?: string) {
  if (!value) return "Never";
  return new Date(value).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

function orgName(orgID: string | undefined, organizations: { id: string; name: string }[]) {
  if (!orgID) return "Unknown org";
  return organizations.find((org) => org.id === orgID)?.name ?? orgID;
}

function SectionHeading({
  title,
  eyebrow,
  actions,
}: {
  title: string;
  eyebrow?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-3 flex items-end justify-between gap-4">
      <Box>
        {eyebrow && (
          <Text c="dimmed" fw={700} size="xs" tt="uppercase">
            {eyebrow}
          </Text>
        )}
        <Title mt={2} order={3} size="h5">
          {title}
        </Title>
      </Box>
      {actions}
    </div>
  );
}

function TableSurface({ children }: { children: ReactNode }) {
  return (
    <Box className="mt-3 overflow-hidden rounded-lg bg-kumo-base ring ring-kumo-line">
      {children}
    </Box>
  );
}

function EmptyState({ icon, title, copy }: { icon: ReactNode; title: string; copy: string }) {
  return (
    <div className="flex min-h-56 items-center justify-center px-5 py-8">
      <Stack align="center" gap={4} ta="center" className="[&_svg]:size-6">
        {icon}
        <Text fw={700} size="sm">
          {title}
        </Text>
        <Text c="dimmed" size="xs">
          {copy}
        </Text>
      </Stack>
    </div>
  );
}
