import { Center, ScrollArea, Stack, Table, Text } from "@mantine/core"
import { KeyRound, Plus } from "lucide-react"
import { useNavigate } from "react-router-dom"
import { useWorkspace } from "../app/workspace-context"
import { PageHeading, Panel } from "../components/shared/primitives"
import { Badge } from "../components/ui/badge"
import { Button } from "../components/ui/button"

export function KVNamespacesPage() {
  const navigate = useNavigate()
  const { namespaces, workers, openNamespaceDialog } = useWorkspace()

  return (
    <>
      <PageHeading eyebrow="Storage" title="KV" copy="Manage namespace inventory for your workers, then drill into a namespace to rename it or inspect its shared data." actions={<Button onClick={openNamespaceDialog}><Plus className="size-4" />New namespace</Button>} />
      <Panel flush>
        <ScrollArea>
          <Table highlightOnHover miw={760} verticalSpacing="sm">
            <Table.Thead><Table.Tr><Table.Th>Namespace</Table.Th><Table.Th>ID</Table.Th><Table.Th>Bindings</Table.Th><Table.Th>Created</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {namespaces.map((namespace) => {
                const boundCount = workers.filter((worker) => worker.bindings?.some((binding) => binding.kind === "kv" && binding.namespace_id === namespace.id)).length
                return (
                  <Table.Tr key={namespace.id} className="cursor-pointer" onClick={() => navigate(`/kv/${namespace.id}`)}>
                    <Table.Td><Text fw={700}>{namespace.name}</Text></Table.Td>
                    <Table.Td><Text c="dimmed" ff="monospace" size="xs">{namespace.id}</Text></Table.Td>
                    <Table.Td><Badge tone={boundCount ? "green" : "orange"}>{boundCount} worker{boundCount === 1 ? "" : "s"}</Badge></Table.Td>
                    <Table.Td><Text c="dimmed" size="sm">{new Date(namespace.created_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</Text></Table.Td>
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
