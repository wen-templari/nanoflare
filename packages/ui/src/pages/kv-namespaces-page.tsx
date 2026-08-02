import { Empty, LayerCard, Table, Text } from "@cloudflare/kumo";
import { KeyRound, Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import type { KVNamespaceMetricsItem } from "../app/types";

import { apiClient } from "../app/api";
import { normalizeUsageLevel, orgLimitsForLevel, usageLevelPaid } from "../app/org-limits";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { formatBytes } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { CopyableResourceID } from "../components/shared/copyable-resource-id";
import { PageHeading } from "../components/shared/primitives";
import { Button } from "../components/ui/button";

export function KVNamespacesPage() {
  const navigate = useNavigate();
  const { activeOrgID, organizations, namespaces, notify, openNamespaceDialog } = useWorkspace();
  useWorkspaceResources(["namespaces"], "list");
  const [metrics, setMetrics] = useState<Record<string, KVNamespaceMetricsItem>>({});
  const activeOrg = organizations.find((org) => org.id === activeOrgID);
  const usageLevel = normalizeUsageLevel(activeOrg?.usage_level);
  const namespaceLimit = orgLimitsForLevel(usageLevel).kvNamespaces;
  const namespaceLimitReached = namespaceLimit !== null && namespaces.length >= namespaceLimit;

  useEffect(() => {
    let cancelled = false;
    setMetrics({});
    if (!activeOrgID) return;
    void apiClient
      .GET("/v1/organizations/{orgID}/kv-namespaces/metrics", {
        params: { path: { orgID: activeOrgID } },
      })
      .then(({ data }) => {
        if (cancelled) return;
        setMetrics(Object.fromEntries((data ?? []).map((item) => [item.namespace_id, item])));
      });
    return () => {
      cancelled = true;
    };
  }, [activeOrgID]);

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
      <LayerCard className="overflow-hidden">
        <div className="overflow-x-auto">
          <Table className="min-w-[760px]" layout="fixed">
            <Table.Header>
              <Table.Row>
                <Table.Head className="w-[30%]">Namespace</Table.Head>
                <Table.Head className="w-[30%]">ID</Table.Head>
                <Table.Head className="w-[22%]">Size</Table.Head>
                <Table.Head className="w-[18%]">Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {namespaces.map((namespace) => {
                const metric = metrics[namespace.id];
                return (
                  <Table.Row
                    key={namespace.id}
                    className="cursor-pointer"
                    onClick={() => navigate(`/kv/${namespace.id}`)}
                  >
                    <Table.Cell className="w-[30%]">
                      <Text DANGEROUS_className="truncate">{namespace.name}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[30%]">
                      <CopyableResourceID
                        value={namespace.id}
                        onCopied={() => notify("Namespace ID copied")}
                      />
                    </Table.Cell>
                    <Table.Cell className="w-[22%]">
                      <Text>{metric?.available ? formatBytes(metric.size) : "—"}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[18%]">
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
      </LayerCard>
    </>
  );
}

function limitReachedText(resource: string, limit: number, usageLevel: string) {
  return usageLevel === usageLevelPaid
    ? `Limit reached: ${limit} ${resource}.`
    : `Default plan limit reached: ${limit} ${resource}.`;
}
