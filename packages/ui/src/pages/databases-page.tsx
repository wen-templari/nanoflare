import { Empty, Table, Text } from "@cloudflare/kumo";
import { Database, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";

import { useWorkspaceResources } from "../app/use-workspace-resources";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading, Panel } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";

export function DatabasesPage() {
  const navigate = useNavigate();
  const { databases, workers, openDatabaseDialog } = useWorkspace();
  useWorkspaceResources(["databases", "workers"], "details");

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
      <Panel flush>
        <div className="overflow-x-auto">
          <Table className="min-w-[760px]" layout="fixed">
            <Table.Header>
              <Table.Row>
                <Table.Head className="w-[30%]">Database</Table.Head>
                <Table.Head className="w-[44%]">ID</Table.Head>
                <Table.Head className="w-[14%]">Bindings</Table.Head>
                <Table.Head className="w-[12%]">Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {databases.map((database) => {
                const boundCount = workers.filter((worker) =>
                  worker.bindings?.some(
                    (binding) => binding.kind === "db" && binding.database_id === database.id,
                  ),
                ).length;
                return (
                  <Table.Row
                    key={database.id}
                    className="cursor-pointer"
                    onClick={() => navigate(`/databases/${database.id}`)}
                  >
                    <Table.Cell className="w-[30%]">
                      <Text DANGEROUS_className="truncate">{database.name}</Text>
                    </Table.Cell>
                    <Table.Cell className="w-[44%]">
                      <Text DANGEROUS_className="truncate" variant="mono-secondary">
                        {database.id}
                      </Text>
                    </Table.Cell>
                    <Table.Cell className="w-[14%]">
                      <Badge tone={boundCount ? "green" : "orange"}>
                        {boundCount} worker{boundCount === 1 ? "" : "s"}
                      </Badge>
                    </Table.Cell>
                    <Table.Cell className="w-[12%]">
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
      </Panel>
    </>
  );
}
