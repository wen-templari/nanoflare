import { Empty, Table, Text } from "@cloudflare/kumo";
import { DatabaseZap, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";

import { normalizeUsageLevel, orgLimitsForLevel, usageLevelPaid } from "../app/org-limits";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function ObjectStorageBucketsPage() {
  const navigate = useNavigate();
  const {
    activeOrgID,
    organizations,
    objectStorageBuckets,
    workers,
    openObjectStorageBucketDialog,
  } = useWorkspace();
  useWorkspaceResources(["objectStorageBuckets", "workers"], "details");
  const activeOrg = organizations.find((org) => org.id === activeOrgID);
  const usageLevel = normalizeUsageLevel(activeOrg?.usage_level);
  const bucketLimit = orgLimitsForLevel(usageLevel).objectStorageBuckets;
  const bucketLimitReached = bucketLimit !== null && objectStorageBuckets.length >= bucketLimit;

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
      <Panel flush>
        <div className="overflow-x-auto">
          <Table className="min-w-[760px]" layout="fixed">
            <Table.Header>
              <Table.Row>
                <Table.Head className="w-[30%]">Bucket</Table.Head>
                <Table.Head className="w-[44%]">ID</Table.Head>
                <Table.Head className="w-[14%]">Bindings</Table.Head>
                <Table.Head className="w-[12%]">Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {objectStorageBuckets.map((bucket) => {
                const boundCount = workers.filter((worker) =>
                  worker.bindings?.some(
                    (binding) =>
                      binding.kind === "object_storage_bucket" && binding.bucket_id === bucket.id,
                  ),
                ).length;
                return (
                  <Table.Row
                    key={bucket.id}
                    className="cursor-pointer"
                    onClick={() => navigate(`/object-storage/${bucket.id}`)}
                  >
                    <Table.Cell className="w-[30%]">
                      <Text DANGEROUS_className="truncate">{bucket.name}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[44%]">
                      <Text DANGEROUS_className="truncate" variant="mono-secondary">
                        {bucket.id}
                      </Text>
                    </Table.Cell>
                    <Table.Cell className="w-[14%]">
                      <Badge tone={boundCount ? "green" : "orange"}>
                        {boundCount} worker{boundCount === 1 ? "" : "s"}
                      </Badge>
                    </Table.Cell>
                    <Table.Cell className="w-[12%]">
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
      </Panel>
    </>
  );
}

function limitReachedText(resource: string, limit: number, usageLevel: string) {
  return usageLevel === usageLevelPaid
    ? `Limit reached: ${limit} ${resource}.`
    : `Default plan limit reached: ${limit} ${resource}.`;
}
