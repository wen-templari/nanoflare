import { Empty, LayerCard, Table, Text } from "@cloudflare/kumo";
import { Database, Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import type { DatabaseMetricsItem } from "../app/types";

import { apiClient } from "../app/api";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { formatBytes } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { CopyableResourceID } from "../components/shared/copyable-resource-id";
import { PageHeading } from "../components/shared/primitives";
import { Button } from "../components/ui/button";

export function DatabasesPage() {
  const navigate = useNavigate();
  const { activeOrgID, databases, notify, openDatabaseDialog } = useWorkspace();
  useWorkspaceResources(["databases"], "list");
  const [metrics, setMetrics] = useState<Record<string, DatabaseMetricsItem>>({});

  useEffect(() => {
    let cancelled = false;
    setMetrics({});
    if (!activeOrgID) return;
    void apiClient
      .GET("/v1/organizations/{orgID}/databases/metrics", {
        params: { path: { orgID: activeOrgID } },
      })
      .then(({ data }) => {
        if (cancelled) return;
        setMetrics(Object.fromEntries((data ?? []).map((item) => [item.database_id, item])));
      });
    return () => {
      cancelled = true;
    };
  }, [activeOrgID]);

  return (
    <>
      <PageHeading
        eyebrow="Storage"
        title="Databases"
        copy="Manage SQLite databases for Worker DB bindings."
        actions={
          <Button onClick={openDatabaseDialog}>
            <Plus className="size-4" />
            Create database
          </Button>
        }
      />
      <LayerCard className="overflow-hidden">
        <div className="overflow-x-auto">
          <Table className="min-w-[760px]" layout="fixed">
            <Table.Header>
              <Table.Row>
                <Table.Head className="w-[26%]">Database</Table.Head>
                <Table.Head className="w-[26%]">ID</Table.Head>
                <Table.Head className="w-[14%]">Tables</Table.Head>
                <Table.Head className="w-[18%]">Size</Table.Head>
                <Table.Head className="w-[16%]">Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {databases.map((database) => {
                const metric = metrics[database.id];
                return (
                  <Table.Row
                    key={database.id}
                    className="cursor-pointer"
                    onClick={() => navigate(`/databases/${database.id}`)}
                  >
                    <Table.Cell className="w-[26%]">
                      <Text DANGEROUS_className="truncate">{database.name}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[26%]">
                      <CopyableResourceID
                        value={database.id}
                        onCopied={() => notify("Database ID copied")}
                      />
                    </Table.Cell>
                    <Table.Cell className="w-[14%]">
                      <Text>{metric?.available ? metric.table_count.toLocaleString() : "—"}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[18%]">
                      <Text>{metric?.available ? formatBytes(metric.storage_bytes) : "—"}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[16%]">
                      <Text DANGEROUS_className="truncate" size="sm" variant="secondary">
                        {new Date(database.created_at).toLocaleDateString(undefined, {
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
        {!databases.length && (
          <Empty
            icon={<Database />}
            title="No databases yet"
            description="Create one to bind SQLite into a worker"
          />
        )}
      </LayerCard>
    </>
  );
}
