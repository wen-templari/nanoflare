import { ActionIcon, Anchor, AppShell, Box, Breadcrumbs, Burger, Button, Group, Modal, NavLink as MantineNavLink, Notification, Paper, Select, Stack, Text, TextInput, Title, Tooltip } from "@mantine/core"
import { useDisclosure } from "@mantine/hooks"
import { Boxes, Check, CircleGauge, DatabaseZap, KeyRound, LogOut, Plus, Settings, Waypoints } from "lucide-react"
import { useState } from "react"
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom"
import { useWorkspace } from "../../app/workspace-context"
import { CreateKVNamespaceDialog } from "../dialogs/create-kv-namespace-dialog"
import { CreateObjectStorageBucketDialog } from "../dialogs/create-object-storage-bucket-dialog"
import { CreateWorkerDialog } from "../dialogs/create-worker-dialog"

const navItems = [
  { href: "/", match: "/", label: "Overview", icon: CircleGauge },
  { href: "/workers", match: "/workers", label: "Workers", icon: Waypoints },
  { href: "/kv", match: "/kv", label: "KV", icon: KeyRound },
  { href: "/object-storage", match: "/object-storage", label: "Object storage", icon: DatabaseZap },
  { href: "/settings", match: "/settings", label: "Settings", icon: Settings },
]

export function ConsoleLayout() {
  const location = useLocation()
  const navigate = useNavigate()
  const [opened, { toggle, close }] = useDisclosure()
  const {
    workers,
    setWorkers,
    namespaces,
    setNamespaces,
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
    objectStorageBucketDialogOpen,
    openWorkerDialog,
    closeWorkerDialog,
    openNamespaceDialog,
    closeNamespaceDialog,
    closeObjectStorageBucketDialog,
    toast,
    notify,
  } = useWorkspace()
  const [orgModalOpen, setOrgModalOpen] = useState(false)
  const [orgName, setOrgName] = useState("")
  const [orgSaving, setOrgSaving] = useState(false)
  const [orgError, setOrgError] = useState("")

  const breadcrumbs = getBreadcrumbs(location.pathname, { workers, namespaces, objectStorageBuckets })
  const hasOrg = Boolean(activeOrgID)

  async function submitOrganization(event: React.FormEvent) {
    event.preventDefault()
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
        onLogout={() => {
          logout()
          navigate("/login", { replace: true })
        }}
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
            <Breadcrumbs>
              {breadcrumbs.map((item, index) => (
                item.href && index < breadcrumbs.length - 1
                  ? <Anchor component={Link} key={item.href} size="sm" to={item.href}>{item.label}</Anchor>
                  : <Text c="dimmed" key={`${item.label}-${index}`} size="sm">{item.label}</Text>
              ))}
            </Breadcrumbs>
          </Group>
          <Group gap="sm">
            <Select
              allowDeselect={false}
              data={organizations.map((org) => ({ value: org.id, label: org.name }))}
              disabled={!organizations.length}
              maw={220}
              onChange={(value) => value && setActiveOrgID(value)}
              placeholder="No organization"
              size="xs"
              value={activeOrgID}
            />
            <Tooltip label="Create organization">
              <ActionIcon aria-label="Create organization" onClick={() => setOrgModalOpen(true)} variant="subtle">
                <Plus size={16} />
              </ActionIcon>
            </Tooltip>
            <Tooltip label="Sign out">
              <ActionIcon
                aria-label="Sign out"
                color="gray"
                onClick={() => {
                  logout()
                  navigate("/login", { replace: true })
                }}
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
      <CreateObjectStorageBucketDialog open={objectStorageBucketDialogOpen} onClose={closeObjectStorageBucketDialog} buckets={objectStorageBuckets} setBuckets={setObjectStorageBuckets} notify={notify} apiConnected={apiConnected} />

      {toast && (
        <Notification className="fixed bottom-5 right-5 z-[60]" color="green" icon={<Check size={16} />} withCloseButton={false}>
          {toast}
        </Notification>
      )}

      <Modal opened={orgModalOpen} onClose={() => setOrgModalOpen(false)} title="Create organization">
        <form onSubmit={submitOrganization}>
          <Stack>
            {orgError && <Text c="red" size="sm">{orgError}</Text>}
            <TextInput label="Name" onChange={(event) => setOrgName(event.currentTarget.value)} required value={orgName} />
            <Group justify="end">
              <ActionIcon aria-label="Create organization" loading={orgSaving} type="submit" variant="filled">
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

  if (section === "object-storage") {
    const bucket = workspace.objectStorageBuckets.find((item) => item.id === id)
    return id ? [{ href: "/object-storage", label: "Object storage" }, { label: bucket?.name ?? id }] : [{ label: "Object storage" }]
  }

  if (section === "settings") {
    return id ? [{ href: "/settings", label: "Settings" }, { label: id === "oauth-clients" ? "OAuth client" : id }] : [{ label: "Settings" }]
  }

  return [{ label: "Overview" }]
}
