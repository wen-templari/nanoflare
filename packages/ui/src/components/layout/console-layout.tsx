import {
  Breadcrumbs,
  Button,
  Dialog,
  Field,
  LayerCard,
  Select,
  Sidebar,
  Text,
  Tooltip,
} from "@cloudflare/kumo";
import {
  Boxes,
  Check,
  CircleGauge,
  Database,
  DatabaseZap,
  KeyRound,
  LogOut,
  Settings,
  Waypoints,
  X,
} from "lucide-react";
import { Fragment, useState } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { normalizeUsageLevel } from "../../app/org-limits";
import { useWorkspace } from "../../app/workspace-context";
import { CreateDatabaseDialog } from "../dialogs/create-database-dialog";
import { CreateKVNamespaceDialog } from "../dialogs/create-kv-namespace-dialog";
import { CreateObjectStorageBucketDialog } from "../dialogs/create-object-storage-bucket-dialog";
import { CreateWorkerDialog } from "../dialogs/create-worker-dialog";
import { Input } from "../ui/input";

const navItems = [
  { href: "/", match: "/", label: "Overview", icon: CircleGauge },
  { href: "/workers", match: "/workers", label: "Workers", icon: Waypoints },
  { href: "/kv", match: "/kv", label: "KV", icon: KeyRound },
  { href: "/databases", match: "/databases", label: "Databases", icon: Database },
  { href: "/object-storage", match: "/object-storage", label: "Object storage", icon: DatabaseZap },
  { href: "/settings", match: "/settings", label: "Settings", icon: Settings },
];

const createOrganizationSelectValue = "__create_organization__";

export function ConsoleLayout() {
  const location = useLocation();
  const navigate = useNavigate();
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
    notify,
  } = useWorkspace();
  const [orgModalOpen, setOrgModalOpen] = useState(false);
  const [orgName, setOrgName] = useState("");
  const [orgSaving, setOrgSaving] = useState(false);
  const [orgError, setOrgError] = useState("");

  const breadcrumbs = getBreadcrumbs(location.pathname, {
    workers,
    namespaces,
    databases,
    objectStorageBuckets,
  });
  const hasOrg = Boolean(activeOrgID);
  const organizationSelectData = [
    ...organizations.map((org) => ({
      value: org.id,
      label: org.name,
      usageLevel: normalizeUsageLevel(org.usage_level),
    })),
    { value: createOrganizationSelectValue, label: "Create organization", usageLevel: "" },
  ];

  function signOut() {
    logout();
    window.location.assign("/v1/auth/oidc/logout");
  }

  async function submitOrganization(event: React.FormEvent) {
    event.preventDefault();
    setOrgSaving(true);
    setOrgError("");
    try {
      await createOrganization(orgName);
      setOrgName("");
      setOrgModalOpen(false);
      notify("Organization created");
      navigate("/");
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : "Could not create organization");
    } finally {
      setOrgSaving(false);
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
      />
    );
  }

  return (
    <Sidebar.Provider className="h-svh overflow-hidden" collapsible="none" defaultOpen>
      <Sidebar>
        <Sidebar.Header>
          <div className="flex w-full items-center gap-2">
            <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-kumo-brand text-kumo-inverse">
              <Boxes size={17} />
            </div>
            <Select
              aria-label="Active organization"
              className="min-w-0 flex-1 !h-8 !justify-start !bg-transparent !px-2 !text-sm !shadow-none !ring-0 hover:!bg-kumo-tint"
              items={organizationSelectData}
              disabled={!organizations.length}
              onValueChange={(value) => {
                if (!value) return;
                if (value === createOrganizationSelectValue) {
                  setOrgModalOpen(true);
                  return;
                }
                setActiveOrgID(value);
              }}
              placeholder="No organization"
              renderValue={(value) => {
                const organization = organizationSelectData.find((item) => item.value === value);
                return <span className="block truncate">{organization?.label ?? "No organization"}</span>;
              }}
              size="sm"
              value={activeOrgID}
            />
          </div>
        </Sidebar.Header>
        <Sidebar.Content>
          <Sidebar.Group>
            <Sidebar.GroupLabel>Console</Sidebar.GroupLabel>
            <Sidebar.Menu>
              {navItems.map(({ href, match, label, icon: Icon }) => (
                <Sidebar.MenuButton
                  active={
                    location.pathname === match ||
                    (match !== "/" && location.pathname.startsWith(match))
                  }
                  icon={Icon}
                  key={href}
                  onClick={() => navigate(href)}
                  tooltip={label}
                >
                  {label}
                </Sidebar.MenuButton>
              ))}
            </Sidebar.Menu>
          </Sidebar.Group>
        </Sidebar.Content>
        <Sidebar.Footer>
          <Tooltip
            content="Sign out"
            render={
              <Button aria-label="Sign out" onClick={signOut} shape="square" variant="ghost">
                <LogOut size={16} />
              </Button>
            }
          />
        </Sidebar.Footer>
      </Sidebar>

      <div className="flex h-svh min-w-0 flex-1 flex-col overflow-hidden bg-kumo-base text-kumo-default">
        <header className="z-20 flex h-[58px] shrink-0 items-center border-b border-kumo-line bg-kumo-base px-5 md:px-8">
          <div className="flex min-w-0 items-center gap-3">
            <Sidebar.Trigger className="md:hidden" />
            <Breadcrumbs>
              {breadcrumbs.map((item, index) => {
                const isCurrent = index === breadcrumbs.length - 1;

                return (
                  <Fragment key={`${item.label}-${index}`}>
                    {isCurrent ? (
                      <Breadcrumbs.Current>{item.label}</Breadcrumbs.Current>
                    ) : (
                      <Breadcrumbs.Link href={item.href ?? "/"}>{item.label}</Breadcrumbs.Link>
                    )}
                    {!isCurrent && <Breadcrumbs.Separator />}
                  </Fragment>
                );
              })}
            </Breadcrumbs>
          </div>
        </header>

        <main className="min-h-0 flex-1 overflow-y-auto p-5 md:p-8">
          <div className="mx-auto w-full max-w-screen-xl">
            <Outlet />
          </div>
        </main>
        <CreateWorkerDialog
          open={workerDialogOpen}
          onClose={closeWorkerDialog}
          workers={workers}
          setWorkers={(nextWorkers) => setWorkers(nextWorkers)}
          notify={notify}
          apiConnected={apiConnected}
        />
        <CreateKVNamespaceDialog
          open={namespaceDialogOpen}
          onClose={closeNamespaceDialog}
          namespaces={namespaces}
          setNamespaces={setNamespaces}
          notify={notify}
          apiConnected={apiConnected}
        />
        <CreateDatabaseDialog
          open={databaseDialogOpen}
          onClose={closeDatabaseDialog}
          databases={databases}
          setDatabases={setDatabases}
          notify={notify}
          apiConnected={apiConnected}
        />
        <CreateObjectStorageBucketDialog
          open={objectStorageBucketDialogOpen}
          onClose={closeObjectStorageBucketDialog}
          buckets={objectStorageBuckets}
          setBuckets={setObjectStorageBuckets}
          notify={notify}
          apiConnected={apiConnected}
        />

        <Dialog.Root open={orgModalOpen} onOpenChange={(open) => !open && setOrgModalOpen(false)}>
          <Dialog className="p-6">
            <div className="flex items-start justify-between gap-4">
              <Dialog.Title className="text-lg font-semibold">Create organization</Dialog.Title>
              <Dialog.Close
                render={(props) => (
                  <Button {...props} aria-label="Close" shape="square" size="sm" variant="ghost">
                    <X className="size-4" />
                  </Button>
                )}
              />
            </div>
            <form className="mt-4 grid gap-4" onSubmit={submitOrganization}>
              {orgError && (
                <Text size="sm" variant="error">
                  {orgError}
                </Text>
              )}
              <Field label="Name">
                <Input
                  onChange={(event) => setOrgName(event.currentTarget.value)}
                  required
                  value={orgName}
                />
              </Field>
              <div className="flex justify-end">
                <Button loading={orgSaving} type="submit">
                  <Check className="size-4" />
                  Create organization
                </Button>
              </div>
            </form>
          </Dialog>
        </Dialog.Root>
      </div>
    </Sidebar.Provider>
  );
}

function OrganizationOnboarding({
  error,
  name,
  onLogout,
  onNameChange,
  onSubmit,
  saving,
}: {
  error: string;
  name: string;
  onLogout: () => void;
  onNameChange: (name: string) => void;
  onSubmit: (event: React.FormEvent) => void;
  saving: boolean;
}) {
  return (
    <div className="min-h-screen bg-kumo-base">
      <header className="flex h-16 items-center justify-between px-8">
        <div className="flex items-center gap-3">
          <div className="grid size-9 place-items-center rounded-md bg-kumo-brand text-kumo-inverse">
            <Boxes size={18} />
          </div>
          <Text as="h2" variant="heading3">
            nanoflare
          </Text>
        </div>
        <Tooltip
          content="Sign out"
          render={
            <Button aria-label="Sign out" onClick={onLogout} shape="square" variant="ghost">
              <LogOut size={16} />
            </Button>
          }
        />
      </header>

      <div className="flex min-h-[calc(100vh-64px)] items-center justify-center gap-14 px-8 py-12">
        <div className="grid max-w-md gap-6">
          <div>
            <Text as="h1" variant="heading1">
              Create your first organization
            </Text>
            <div className="mt-4">
              <Text variant="secondary">
                Start with a workspace for your team, resources, OAuth clients, and member access.
                You can create more organizations later.
              </Text>
            </div>
          </div>
          <div className="grid gap-2">
            <GuideStep
              label="1"
              title="Name the organization"
              copy="Use a team, company, project, or environment name."
            />
            <GuideStep
              label="2"
              title="Become the owner"
              copy="You receive owner access and can invite other users next."
            />
            <GuideStep
              label="3"
              title="Build in the console"
              copy="Workers, KV, object storage, and settings will open after creation."
            />
          </div>
        </div>

        <LayerCard className="w-full max-w-[430px] px-6 py-5 shadow-sm ring ring-kumo-line">
          <form className="grid gap-4" onSubmit={onSubmit}>
            <div className="grid gap-1.5">
              <Text as="h2" variant="heading3">
                Organization details
              </Text>
              <Text size="sm" variant="secondary">
                This creates a new org and selects it immediately.
              </Text>
            </div>
            {error && (
              <Text size="sm" variant="error">
                {error}
              </Text>
            )}
            <Field label="Organization name">
              <Input
                autoFocus
                onChange={(event) => onNameChange(event.currentTarget.value)}
                placeholder="Acme Production"
                required
                value={name}
              />
            </Field>
            <div className="flex justify-end">
              <Button loading={saving} type="submit">
                <Check className="size-4" />
                Create organization
              </Button>
            </div>
          </form>
        </LayerCard>
      </div>
    </div>
  );
}

function GuideStep({ label, title, copy }: { label: string; title: string; copy: string }) {
  return (
    <div className="flex items-start gap-3">
      <div className="grid size-7 shrink-0 place-items-center rounded-md bg-kumo-base text-sm font-semibold text-kumo-info ring ring-kumo-line">
        {label}
      </div>
      <div>
        <Text as="h3" bold size="sm">
          {title}
        </Text>
        <Text size="sm" variant="secondary">
          {copy}
        </Text>
      </div>
    </div>
  );
}

function getBreadcrumbs(
  pathname: string,
  workspace: {
    objectStorageBuckets: { id: string; name: string }[];
    databases: { id: string; name: string }[];
    namespaces: { id: string; name: string }[];
    workers: { id: string; name: string }[];
  },
) {
  const [, section, id] = pathname.split("/");

  if (!section) return [{ label: "Overview" }];

  if (section === "workers") {
    const worker = workspace.workers.find((item) => item.id === id);
    return id
      ? [{ href: "/workers", label: "Workers" }, { label: worker?.name ?? id }]
      : [{ label: "Workers" }];
  }

  if (section === "kv") {
    const namespace = workspace.namespaces.find((item) => item.id === id);
    return id
      ? [{ href: "/kv", label: "KV" }, { label: namespace?.name ?? id }]
      : [{ label: "KV" }];
  }

  if (section === "databases") {
    const database = workspace.databases.find((item) => item.id === id);
    return id
      ? [{ href: "/databases", label: "Databases" }, { label: database?.name ?? id }]
      : [{ label: "Databases" }];
  }

  if (section === "object-storage") {
    const bucket = workspace.objectStorageBuckets.find((item) => item.id === id);
    return id
      ? [{ href: "/object-storage", label: "Object storage" }, { label: bucket?.name ?? id }]
      : [{ label: "Object storage" }];
  }

  if (section === "settings") {
    return id
      ? [
          { href: "/settings", label: "Settings" },
          { label: id === "oauth-clients" ? "OAuth client" : id },
        ]
      : [{ label: "Settings" }];
  }

  return [{ label: "Overview" }];
}
