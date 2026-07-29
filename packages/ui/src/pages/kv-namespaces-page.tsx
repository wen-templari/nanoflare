import { Empty, Table, Text } from "@cloudflare/kumo";
import { KeyRound, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { normalizeUsageLevel, orgLimitsForLevel, usageLevelPaid } from "../app/org-limits";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function KVNamespacesPage() {
  const navigate = useNavigate();
  const { activeOrgID, organizations, namespaces, workers, openNamespaceDialog } = useWorkspace();
  const activeOrg = organizations.find((org) => org.id === activeOrgID);
  const usageLevel = normalizeUsageLevel(activeOrg?.usage_level);
  const namespaceLimit = orgLimitsForLevel(usageLevel).kvNamespaces;
  const namespaceLimitReached = namespaceLimit !== null && namespaces.length >= namespaceLimit;

  return (
    <>
      <PageHeading
        eyebrow="Storage"
        title="KV"
        copy="Manage KV namespace inventory for your workers."
        actions={
          namespaceLimitReached ? (
            <Text size="sm" variant="secondary">
              {limitReachedText("KV namespaces", namespaceLimit, usageLevel)}
            </Text>
          ) : (
            <Button onClick={openNamespaceDialog}>
              <Plus className="size-4" />
              Create namespace
            </Button>
          )
        }
      />
      <Panel flush>
        <div className="overflow-x-auto">
          <Table className="min-w-[760px]" layout="fixed">
            <Table.Header>
              <Table.Row>
                <Table.Head className="w-[30%]">Namespace</Table.Head>
                <Table.Head className="w-[44%]">ID</Table.Head>
                <Table.Head className="w-[14%]">Bindings</Table.Head>
                <Table.Head className="w-[12%]">Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {namespaces.map((namespace) => {
                const boundCount = workers.filter((worker) =>
                  worker.bindings?.some(
                    (binding) => binding.kind === "kv" && binding.namespace_id === namespace.id,
                  ),
                ).length;
                return (
                  <Table.Row
                    key={namespace.id}
                    className="cursor-pointer"
                    onClick={() => navigate(`/kv/${namespace.id}`)}
                  >
                    <Table.Cell className="w-[30%]">
                      <Text DANGEROUS_className="truncate">{namespace.name}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[44%]">
                      <Text DANGEROUS_className="truncate" variant="mono-secondary">
                        {namespace.id}
                      </Text>
                    </Table.Cell>
                    <Table.Cell className="w-[14%]">
                      <Badge tone={boundCount ? "green" : "orange"}>
                        {boundCount} worker{boundCount === 1 ? "" : "s"}
                      </Badge>
                    </Table.Cell>
                    <Table.Cell className="w-[12%]">
                      <Text DANGEROUS_className="truncate" size="sm" variant="secondary">
                        {new Date(namespace.created_at).toLocaleDateString(undefined, {
                          month: "short",
                          day: "numeric",
                        })}
                      </Text>
                    </Table.Cell>
                  </Table.Row>
                );
              })}
            </Table.Body>
          </Table>
        </div>
        {!namespaces.length && (
          <Empty
            icon={<KeyRound />}
            title="No namespaces yet"
            description="Create one to bind KV storage into a worker"
          />
        )}
      </Panel>
    </>
  );
}

function limitReachedText(resource: string, limit: number, usageLevel: string) {
  return usageLevel === usageLevelPaid
    ? `Limit reached: ${limit} ${resource}.`
    : `Default plan limit reached: ${limit} ${resource}.`;
}
