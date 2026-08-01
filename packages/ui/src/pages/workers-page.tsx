import { Link, Table, Text } from "@cloudflare/kumo";
import { ChevronRight, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";

import type { Worker } from "../app/types";

import { normalizeUsageLevel, orgLimitsForLevel, usageLevelPaid } from "../app/org-limits";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function WorkersPage() {
  const navigate = useNavigate();
  const { activeOrgID, organizations, workers, openWorkerDialog } = useWorkspace();
  const activeOrg = organizations.find((org) => org.id === activeOrgID);
  const usageLevel = normalizeUsageLevel(activeOrg?.usage_level);
  const workerLimit = orgLimitsForLevel(usageLevel).workers;
  const workerLimitReached = workerLimit !== null && workers.length >= workerLimit;

  return (
    <>
      <PageHeading
        eyebrow="Runtime"
        title="Workers"
        copy="Register isolated services, deploy bundles, and watch the runtime pool."
        actions={
          workerLimitReached ? (
            <Text size="sm" variant="secondary">
              {limitReachedText("workers", workerLimit, usageLevel)}
            </Text>
          ) : (
            <Button onClick={openWorkerDialog}>
              <Plus className="size-4" />
              Create worker
            </Button>
          )
        }
      />
      <Panel flush>
        <div className="overflow-x-auto">
          <Table className="min-w-[720px]">
            <Table.Header>
              <Table.Row>
                <Table.Head>Worker</Table.Head>
                <Table.Head>State</Table.Head>
                <Table.Head>Requests (24h)</Table.Head>
                <Table.Head>Deployment</Table.Head>
                <Table.Head>Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {workers.map((worker) => (
                <WorkerRow
                  key={worker.id}
                  worker={worker}
                  onSelect={() => navigate(`/workers/${worker.id}`)}
                />
              ))}
            </Table.Body>
          </Table>
        </div>
      </Panel>
    </>
  );
}

function WorkerRow({ worker, onSelect }: { worker: Worker; onSelect: () => void }) {
  return (
    <Table.Row className="cursor-pointer" onClick={onSelect}>
      <Table.Cell>
        <div>
          <Text>{worker.name}</Text>
          <Link
            href={hostnameHref(worker.hostname)}
            onClick={(event) => event.stopPropagation()}
            target="_blank"
            variant="plain"
            className="text-gray-300 hover:underline"
          >
            {worker.hostname}
          </Link>
        </div>
      </Table.Cell>
      <Table.Cell>
        <Badge tone={worker.status === "draft" ? "orange" : "green"}>
          {worker.status ?? "live"}
        </Badge>
      </Table.Cell>
      <Table.Cell>
        <Text variant="mono">{worker.requests ?? "0"}</Text>
      </Table.Cell>
      <Table.Cell>
        <Text variant="mono-secondary">{worker.deployment ?? "awaiting deploy"}</Text>
      </Table.Cell>
      <Table.Cell>
        <Text size="sm" variant="secondary">
          {new Date(worker.created_at).toLocaleDateString(undefined, {
            month: "short",
            day: "numeric",
          })}
        </Text>
      </Table.Cell>
    </Table.Row>
  );
}

function hostnameHref(hostname: string) {
  return /^https?:\/\//i.test(hostname) ? hostname : `https://${hostname}`;
}

function limitReachedText(resource: string, limit: number, usageLevel: string) {
  return usageLevel === usageLevelPaid
    ? `Limit reached: ${limit} ${resource}.`
    : `Default plan limit reached: ${limit} ${resource}.`;
}
