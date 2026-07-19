import { ActionIcon, Anchor, AppShell, Badge, Box, Breadcrumbs, Burger, Button, Group, Modal, NavLink as MantineNavLink, Notification, Paper, Select, Stack, Text, TextInput, Title, Tooltip } from "@mantine/core"
import { useDisclosure } from "@mantine/hooks"
import { Boxes, Check, ChevronDown, CircleGauge, Database, DatabaseZap, KeyRound, LogOut, Plus, Settings, Waypoints } from "lucide-react"
import { useState } from "react"
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom"
import { normalizeUsageLevel, usageLevelPaid } from "../../app/org-limits"
import { useWorkspace } from "../../app/workspace-context"
import { CreateDatabaseDialog } from "../dialogs/create-database-dialog"
import { CreateKVNamespaceDialog } from "../dialogs/create-kv-namespace-dialog"
import { CreateObjectStorageBucketDialog } from "../dialogs/create-object-storage-bucket-dialog"
import { CreateWorkerDialog } from "../dialogs/create-worker-dialog"

const navItems = [
  { href: "/", match: "/", label: "Overview", icon: CircleGauge },
  { href: "/workers", match: "/workers", label: "Workers", icon: Waypoints },
  { href: "/kv", match: "/kv", label: "KV", icon: KeyRound },
  { href: "/databases", match: "/databases", label: "Databases", icon: Database },
  { href: "/object-storage", match: "/object-storage", label: "Object storage", icon: DatabaseZap },
  { href: "/settings", match: "/settings", label: "Settings", icon: Settings },
]

const defaultOwnedOrganizationLimit = 1
const createOrganizationSelectValue = "__create_organization__"

export function ConsoleLayout() {
  const location = useLocation()
  const navigate = useNavigate()
  const [opened, { toggle, close }] = useDisclosure()
  const {
    workers,
    setWorkers,
    namespaces,
    setNamespaces,
    databases,
    setDatabases,
    objectStorageBuckets,
    setObjectStorageBuckets,
    apiConnected,
    activeOrgID,
    organizations,
    setActiveOrgID,
    createOrganization,
    logout,
    workerDialogOpen,
    namespaceDialogOpen,
    databaseDialogOpen,
    objectStorageBucketDialogOpen,
    openWorkerDialog,
    closeWorkerDialog,
    openNamespaceDialog,
    closeNamespaceDialog,
    closeDatabaseDialog,
    closeObjectStorageBucketDialog,
    toast,
    notify,
  } = useWorkspace()
  const [orgModalOpen, setOrgModalOpen] = useState(false)
  const [orgName, setOrgName] = useState("")
  const [orgSaving, setOrgSaving] = useState(false)
  const [orgError, setOrgError] = useState("")

  const breadcrumbs = getBreadcrumbs(location.pathname, { workers, namespaces, databases, objectStorageBuckets })
  const hasOrg = Boolean(activeOrgID)
  const activeOrg = organizations.find((org) => org.id === activeOrgID)
  const activeUsageLevel = normalizeUsageLevel(activeOrg?.usage_level)
  const ownedOrganizations = organizations.filter((org) => org.role === "owner")
  const ownedOrganizationLimitReached = activeUsageLevel !== usageLevelPaid && ownedOrganizations.length >= defaultOwnedOrganizationLimit
  const organizationSelectData = [
    ...organizations.map((org) => ({
      value: org.id,
      label: org.name,
      usageLevel: normalizeUsageLevel(org.usage_level),
    })),
    ...(!ownedOrganizationLimitReached
      ? [{ value: createOrganizationSelectValue, label: "Create organization", usageLevel: "" }]
      : []),
  ]

  function signOut() {
    logout()
    window.location.assign("/v1/auth/oidc/logout")
  }

  async function submitOrganization(event: React.FormEvent) {
    event.preventDefault()
    if (ownedOrganizationLimitReached) {
      setOrgError(`Default users are limited to ${defaultOwnedOrganizationLimit} owned organization.`)
      return
    }
    setOrgSaving(true)
    setOrgError("")
    try {
      await createOrganization(orgName)
      setOrgName("")
      setOrgModalOpen(false)
      notify("Organization created")
      navigate("/")
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : "Could not create organization")
    } finally {
      setOrgSaving(false)
    }
  }

  if (!hasOrg) {
    return (
      <OrganizationOnboarding
        error={orgError}
        name={orgName}
        onLogout={signOut}
        onNameChange={setOrgName}
        onSubmit={submitOrganization}
        saving={orgSaving}
        toast={toast}
      />
    )
  }

  return (
    <AppShell
      header={{ height: 64 }}
      layout="alt"
      navbar={{ width: 260, breakpoint: "md", collapsed: { mobile: !opened } }}
      padding="lg"
    >
      <AppShell.Header>
        <Group h="100%" px="lg" justify="space-between">
          <Group>
            <Burger opened={opened} onClick={toggle} hiddenFrom="md" size="sm" />
            {breadcrumbs.length > 1 && (
              <Breadcrumbs>
                {breadcrumbs.map((item, index) => (
                  item.href && index < breadcrumbs.length - 1
                    ? <Anchor c="gray.5" component={Link} key={item.href} size="sm" to={item.href}>{item.label}</Anchor>
                    : <Text c="gray.9" fw={700} key={`${item.label}-${index}`} size="sm">{item.label}</Text>
                ))}
              </Breadcrumbs>
            )}
          </Group>
          <Group gap="sm">
            <Select
              allowDeselect={false}
              data={organizationSelectData}
              disabled={!organizations.length}
              maw={220}
              onChange={(value) => {
                if (!value) return
                if (value === createOrganizationSelectValue) {
                  setOrgModalOpen(true)
                  return
                }
                setActiveOrgID(value)
              }}
              placeholder="No organization"
              renderOption={({ option }) => {
                if (option.value === createOrganizationSelectValue) {
                  return (
                    <Group gap="xs" wrap="nowrap">
                      <Plus size={14} />
                      <Text size="sm">{option.label}</Text>
                    </Group>
                  )
                }

                const usageLevel = "usageLevel" in option ? option.usageLevel : ""

                return (
                  <Group gap="xs" justify="space-between" w="100%" wrap="nowrap">
                    <Text size="sm" truncate>{option.label}</Text>
                    <Badge color={usageLevel === usageLevelPaid ? "green" : "gray"} radius="sm" size="xs" variant="light">
                      {usageLevel === usageLevelPaid ? "Paid" : "Default"}
                    </Badge>
                  </Group>
                )
              }}
              rightSection={activeOrg ? (
                <Group gap={6} mr={4} wrap="nowrap">
                  <Badge color={activeUsageLevel === usageLevelPaid ? "green" : "gray"} radius="sm" size="xs" variant="light">
                    {activeUsageLevel === usageLevelPaid ? "Paid" : "Default"}
                  </Badge>
                  <ChevronDown size={14} />
                </Group>
              ) : undefined}
              rightSectionPointerEvents="none"
              rightSectionWidth={86}
              size="xs"
              styles={{
                input: { paddingRight: activeOrg ? 90 : undefined },
              }}
              value={activeOrgID}
            />
            <Tooltip label="Sign out">
              <ActionIcon
                aria-label="Sign out"
                color="gray"
                onClick={signOut}
                variant="subtle"
              >
                <LogOut size={16} />
              </ActionIcon>
            </Tooltip>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="md">
        <Group mb="xl">
          <Box bg="blue" c="white" className="grid size-9 place-items-center rounded-md">
            <Boxes size={18} />
          </Box>
          <Box>
            <Title order={2} size="h4">nanoflare</Title>
          </Box>
        </Group>
        <div className="flex flex-col gap-1">
          {navItems.map(({ href, match, label, icon: Icon }) => {
            const active = location.pathname === match || (match !== "/" && location.pathname.startsWith(match))

            return (
              <MantineNavLink
                active={active}
                className="w-full rounded-xl px-4 py-3"
                component={NavLink}
                key={href}
                label={label}
                leftSection={<Icon size={18} />}
                onClick={close}
                to={href}
              />
            )
          })}
        </div>
      </AppShell.Navbar>

      <AppShell.Main>
        <Box maw={1280} mx="auto">
          <Outlet />
        </Box>
      </AppShell.Main>

      <CreateWorkerDialog open={workerDialogOpen} onClose={closeWorkerDialog} workers={workers} setWorkers={(nextWorkers) => setWorkers(nextWorkers)} notify={notify} apiConnected={apiConnected} />
      <CreateKVNamespaceDialog open={namespaceDialogOpen} onClose={closeNamespaceDialog} namespaces={namespaces} setNamespaces={setNamespaces} notify={notify} apiConnected={apiConnected} />
      <CreateDatabaseDialog open={databaseDialogOpen} onClose={closeDatabaseDialog} databases={databases} setDatabases={setDatabases} notify={notify} apiConnected={apiConnected} />
      <CreateObjectStorageBucketDialog open={objectStorageBucketDialogOpen} onClose={closeObjectStorageBucketDialog} buckets={objectStorageBuckets} setBuckets={setObjectStorageBuckets} notify={notify} apiConnected={apiConnected} />

      {toast && (
        <Notification className="fixed bottom-5 right-5 z-[60]" color="green" icon={<Check size={16} />} withCloseButton={false}>
          {toast}
        </Notification>
      )}

      <Modal opened={orgModalOpen} onClose={() => setOrgModalOpen(false)} title="Create organization">
        <form onSubmit={submitOrganization}>
          <Stack>
            {ownedOrganizationLimitReached && (
              <Text c="dimmed" size="sm">Default users are limited to {defaultOwnedOrganizationLimit} owned organization.</Text>
            )}
            {orgError && <Text c="red" size="sm">{orgError}</Text>}
            <TextInput label="Name" onChange={(event) => setOrgName(event.currentTarget.value)} required value={orgName} />
            <Group justify="end">
              <ActionIcon aria-label="Create organization" disabled={ownedOrganizationLimitReached} loading={orgSaving} type="submit" variant="filled">
                <Check size={16} />
              </ActionIcon>
            </Group>
          </Stack>
        </form>
      </Modal>
    </AppShell>
  )
}

function OrganizationOnboarding({
  error,
  name,
  onLogout,
  onNameChange,
  onSubmit,
  saving,
  toast,
}: {
  error: string
  name: string
  onLogout: () => void
  onNameChange: (name: string) => void
  onSubmit: (event: React.FormEvent) => void
  saving: boolean
  toast: string
}) {
  return (
    <Box className="min-h-screen bg-[var(--mantine-color-gray-0)]">
      <Group h={64} px="xl" justify="space-between">
        <Group gap="sm">
          <Box bg="blue" c="white" className="grid size-9 place-items-center rounded-md">
            <Boxes size={18} />
          </Box>
          <Box>
            <Title order={2} size="h4">nanoflare</Title>
          </Box>
        </Group>
        <Tooltip label="Sign out">
          <ActionIcon aria-label="Sign out" color="gray" onClick={onLogout} variant="subtle">
            <LogOut size={16} />
          </ActionIcon>
        </Tooltip>
      </Group>

      <Group align="center" className="min-h-[calc(100vh-64px)]" gap={56} justify="center" px="xl" py={48}>
        <Stack gap="lg" maw={500}>
          <Box>
            <Title order={1} size="h1">Create your first organization</Title>
            <Text c="dimmed" mt="md" size="md">
              Start with a workspace for your team, resources, OAuth clients, and member access. You can create more organizations later.
            </Text>
          </Box>
          <Stack gap="xs">
            <GuideStep label="1" title="Name the organization" copy="Use a team, company, project, or environment name." />
            <GuideStep label="2" title="Become the owner" copy="You receive owner access and can invite other users next." />
            <GuideStep label="3" title="Build in the console" copy="Workers, KV, object storage, and settings will open after creation." />
          </Stack>
        </Stack>

        <Paper bg="white" p="xl" radius="lg" shadow="xs" withBorder w="100%" maw={430}>
          <form onSubmit={onSubmit}>
            <Stack>
              <Box>
                <Title order={2} size="h3">Organization details</Title>
                <Text c="dimmed" size="sm">This creates a new org and selects it immediately.</Text>
              </Box>
              {error && <Text c="red" size="sm">{error}</Text>}
              <TextInput
                autoFocus
                label="Organization name"
                onChange={(event) => onNameChange(event.currentTarget.value)}
                placeholder="Acme Production"
                required
                value={name}
              />
              <Button leftSection={<Check size={16} />} loading={saving} type="submit">
                Create organization
              </Button>
            </Stack>
          </form>
        </Paper>
      </Group>

      {toast && (
        <Notification className="fixed bottom-5 right-5 z-[60]" color="green" icon={<Check size={16} />} withCloseButton={false}>
          {toast}
        </Notification>
      )}
    </Box>
  )
}

function GuideStep({ label, title, copy }: { label: string; title: string; copy: string }) {
  return (
    <Group align="start" gap="sm" wrap="nowrap">
      <Box bg="white" c="blue" className="grid size-7 shrink-0 place-items-center rounded-md border border-[var(--mantine-color-gray-3)] text-sm font-bold">
        {label}
      </Box>
      <Box>
        <Text fw={700} size="sm">{title}</Text>
        <Text c="dimmed" size="sm">{copy}</Text>
      </Box>
    </Group>
  )
}

function getBreadcrumbs(
  pathname: string,
  workspace: {
    objectStorageBuckets: { id: string; name: string }[]
    databases: { id: string; name: string }[]
    namespaces: { id: string; name: string }[]
    workers: { id: string; name: string }[]
  },
) {
  const [, section, id] = pathname.split("/")

  if (!section) return [{ label: "Overview" }]

  if (section === "workers") {
    const worker = workspace.workers.find((item) => item.id === id)
    return id ? [{ href: "/workers", label: "Workers" }, { label: worker?.name ?? id }] : [{ label: "Workers" }]
  }

  if (section === "kv") {
    const namespace = workspace.namespaces.find((item) => item.id === id)
    return id ? [{ href: "/kv", label: "KV" }, { label: namespace?.name ?? id }] : [{ label: "KV" }]
  }

  if (section === "databases") {
    const database = workspace.databases.find((item) => item.id === id)
    return id ? [{ href: "/databases", label: "Databases" }, { label: database?.name ?? id }] : [{ label: "Databases" }]
  }

  if (section === "object-storage") {
    const bucket = workspace.objectStorageBuckets.find((item) => item.id === id)
    return id ? [{ href: "/object-storage", label: "Object storage" }, { label: bucket?.name ?? id }] : [{ label: "Object storage" }]
  }

  if (section === "settings") {
    return id ? [{ href: "/settings", label: "Settings" }, { label: id === "oauth-clients" ? "OAuth client" : id }] : [{ label: "Settings" }]
  }

  return [{ label: "Overview" }]
}
