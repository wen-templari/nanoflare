import { Center, ScrollArea, Stack, Table, Text } from "@mantine/core"
import { KeyRound, Plus } from "lucide-react"
import { useNavigate } from "react-router-dom"
import { normalizeUsageLevel, orgLimitsForLevel, usageLevelPaid } from "../app/org-limits"
import { useWorkspace } from "../app/workspace-context"
import { PageHeading, Panel } from "../components/shared/primitives"
import { Badge } from "../components/ui/badge"
import { Button } from "../components/ui/button"

export function KVNamespacesPage() {
  const navigate = useNavigate()
  const { activeOrgID, organizations, namespaces, workers, openNamespaceDialog } = useWorkspace()
  const activeOrg = organizations.find((org) => org.id === activeOrgID)
  const usageLevel = normalizeUsageLevel(activeOrg?.usage_level)
  const namespaceLimit = orgLimitsForLevel(usageLevel).kvNamespaces
  const namespaceLimitReached = namespaceLimit !== null && namespaces.length >= namespaceLimit

  return (
    <>
      <PageHeading
        eyebrow="Storage"
        title="KV"
        copy="Manage KV namespace inventory for your workers."
        actions={namespaceLimitReached
          ? <Text c="dimmed" size="sm">{limitReachedText("KV namespaces", namespaceLimit, usageLevel)}</Text>
          : <Button onClick={openNamespaceDialog}><Plus className="size-4" />New namespace</Button>}
      />
      <Panel flush>
        <ScrollArea>
          <Table highlightOnHover miw={760} verticalSpacing="sm" className="table-fixed">
            <Table.Thead><Table.Tr><Table.Th className="w-[30%]">Namespace</Table.Th><Table.Th className="w-[44%]">ID</Table.Th><Table.Th className="w-[14%]">Bindings</Table.Th><Table.Th className="w-[12%]">Created</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {namespaces.map((namespace) => {
                const boundCount = workers.filter((worker) => worker.bindings?.some((binding) => binding.kind === "kv" && binding.namespace_id === namespace.id)).length
                return (
                  <Table.Tr key={namespace.id} className="cursor-pointer" onClick={() => navigate(`/kv/${namespace.id}`)}>
                    <Table.Td className="w-[30%]"><Text fw={700} truncate>{namespace.name}</Text></Table.Td>
                    <Table.Td className="w-[44%]"><Text c="dimmed" ff="monospace" size="xs" truncate>{namespace.id}</Text></Table.Td>
                    <Table.Td className="w-[14%]"><Badge tone={boundCount ? "green" : "orange"}>{boundCount} worker{boundCount === 1 ? "" : "s"}</Badge></Table.Td>
                    <Table.Td className="w-[12%]"><Text c="dimmed" size="sm" truncate>{new Date(namespace.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Text></Table.Td>
                  </Table.Tr>
                )
              })}
            </Table.Tbody>
          </Table>
        </ScrollArea>
        {!namespaces.length && <Center h={240}><Stack align="center" gap={4}><KeyRound size={22} /><Text fw={700} size="sm">No namespaces yet</Text><Text c="dimmed" size="xs">Create one to bind KV storage into a worker</Text></Stack></Center>}
      </Panel>
    </>
  )
}

function limitReachedText(resource: string, limit: number, usageLevel: string) {
  return usageLevel === usageLevelPaid ? `Limit reached: ${limit} ${resource}.` : `Default plan limit reached: ${limit} ${resource}.`
}
