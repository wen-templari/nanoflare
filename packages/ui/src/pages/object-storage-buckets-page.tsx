import { Empty, LayerCard, Table, Text } from "@cloudflare/kumo";
import { DatabaseZap, Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import type { ObjectStorageBucketMetricsItem } from "../app/types";

import { apiClient } from "../app/api";
import { normalizeUsageLevel, orgLimitsForLevel, usageLevelPaid } from "../app/org-limits";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { formatBytes } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { CopyableResourceID } from "../components/shared/copyable-resource-id";
import { PageHeading } from "../components/shared/primitives";
import { Button } from "../components/ui/button";

export function ObjectStorageBucketsPage() {
  const navigate = useNavigate();
  const {
    activeOrgID,
    organizations,
    objectStorageBuckets,
    notify,
    openObjectStorageBucketDialog,
  } = useWorkspace();
  useWorkspaceResources(["objectStorageBuckets"], "list");
  const [metrics, setMetrics] = useState<Record<string, ObjectStorageBucketMetricsItem>>({});
  const activeOrg = organizations.find((org) => org.id === activeOrgID);
  const usageLevel = normalizeUsageLevel(activeOrg?.usage_level);
  const bucketLimit = orgLimitsForLevel(usageLevel).objectStorageBuckets;
  const bucketLimitReached = bucketLimit !== null && objectStorageBuckets.length >= bucketLimit;

  useEffect(() => {
    let cancelled = false;
    setMetrics({});
    if (!activeOrgID) return;
    void apiClient
      .GET("/v1/organizations/{orgID}/object-storage-buckets/metrics", {
        params: { path: { orgID: activeOrgID } },
      })
      .then(({ data }) => {
        if (cancelled) return;
        setMetrics(Object.fromEntries((data ?? []).map((item) => [item.bucket_id, item])));
      });
    return () => {
      cancelled = true;
    };
  }, [activeOrgID]);

  return (
    <>
      <PageHeading
        eyebrow="Storage"
        title="Object storage"
        copy="Manage bucket inventory for your workers."
        actions={
          bucketLimitReached ? (
            <Text size="sm" variant="secondary">
              {limitReachedText("object buckets", bucketLimit, usageLevel)}
            </Text>
          ) : (
            <Button onClick={openObjectStorageBucketDialog}>
              <Plus className="size-4" />
              Create bucket
            </Button>
          )
        }
      />
      <LayerCard className="overflow-hidden">
        <div className="overflow-x-auto">
          <Table className="min-w-[760px]" layout="fixed">
            <Table.Header>
              <Table.Row>
                <Table.Head className="w-[24%]">Bucket</Table.Head>
                <Table.Head className="w-[24%]">ID</Table.Head>
                <Table.Head className="w-[14%]">Objects</Table.Head>
                <Table.Head className="w-[20%]">Size</Table.Head>
                <Table.Head className="w-[18%]">Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {objectStorageBuckets.map((bucket) => {
                const metric = metrics[bucket.id];
                return (
                  <Table.Row
                    key={bucket.id}
                    className="cursor-pointer"
                    onClick={() => navigate(`/object-storage/${bucket.id}`)}
                  >
                    <Table.Cell className="w-[24%]">
                      <Text DANGEROUS_className="truncate">{bucket.name}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[24%]">
                      <CopyableResourceID
                        value={bucket.id}
                        onCopied={() => notify("Bucket ID copied")}
                      />
                    </Table.Cell>
                    <Table.Cell className="w-[14%]">
                      <Text>{metric?.available ? metric.object_count.toLocaleString() : "—"}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[20%]">
                      <Text>{metric?.available ? formatBytes(metric.size) : "—"}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[18%]">
                      <Text DANGEROUS_className="truncate" size="sm" variant="secondary">
                        {new Date(bucket.created_at).toLocaleDateString(undefined, {
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
        {!objectStorageBuckets.length && (
          <Empty
            icon={<DatabaseZap />}
            title="No buckets yet"
            description="Create one to bind object storage into a worker"
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
