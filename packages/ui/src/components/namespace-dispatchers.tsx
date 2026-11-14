import type { components } from "@nanoflare/schema";

import { Banner, LayerCard, Table, Text } from "@cloudflare/kumo";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { apiClient, errorMessage } from "../app/api";
import { useAuth } from "../app/auth-context";

type Row = {
  worker: components["schemas"]["App"];
  revision: string;
  traffic: number;
  binding: string;
  outbound?: string;
};

export function NamespaceDispatchers({
  namespaceID,
  revision,
}: {
  namespaceID: string;
  revision: number;
}) {
  const { activeOrgID, organizations } = useAuth();
  const canRead =
    organizations.find((org) => org.id === activeOrgID)?.scopes?.includes("workers:read") ?? false;
  const [rows, setRows] = useState<Row[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    let current = true;
    setRows([]);
    setError("");
    setLoading(true);
    if (!canRead) {
      setLoading(false);
      return;
    }
    void (async () => {
      const workers = await apiClient.GET("/v1/organizations/{orgID}/workers", {
        params: { path: { orgID: activeOrgID } },
      });
      if (workers.error) throw new Error(errorMessage(workers.error, "Could not load dispatchers"));
      const ordinary = (workers.data ?? []).filter((worker) => !worker.platform);
      const found: Row[] = [];
      // Bound control-plane concurrency for organizations with many Workers.
      let index = 0;
      await Promise.all(
        Array.from({ length: Math.min(4, ordinary.length) }, async () => {
          while (current && index < ordinary.length) {
            const worker = ordinary[index++];
            const response = await apiClient.GET(
              "/v1/organizations/{orgID}/workers/{workerID}/active-bindings",
              { params: { path: { orgID: activeOrgID, workerID: worker.id } } },
            );
            if (response.error)
              throw new Error(
                errorMessage(
                  response.error,
                  "Some dispatcher revisions could not be inspected. Refresh to retry.",
                ),
              );
            for (const deployment of response.data ?? [])
              for (const binding of deployment.dispatch_namespaces ?? []) {
                if (binding.namespace_id === namespaceID)
                  found.push({
                    worker,
                    revision: deployment.id,
                    traffic: deployment.traffic_percent,
                    binding: binding.binding,
                    outbound: binding.outbound?.worker_id,
                  });
              }
          }
        }),
      );
      if (current)
        setRows(
          found.sort(
            (a, b) =>
              a.worker.name.localeCompare(b.worker.name) || a.binding.localeCompare(b.binding),
          ),
        );
    })()
      .catch((err) => {
        if (current) setError(err instanceof Error ? err.message : "Could not load dispatchers");
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [activeOrgID, namespaceID, revision, canRead]);
  return (
    <section className="grid min-w-0 gap-3">
      <div className="grid gap-1.5">
        <Text as="h2" variant="heading3">
          Dispatchers
        </Text>
        <Text variant="secondary">Traffic-bearing revisions with bindings to this namespace.</Text>
      </div>
      {error && <Banner variant="error">{error}</Banner>}
      {!canRead ? (
        <Text variant="secondary">
          The workers:read permission is required to inspect dispatcher references.
        </Text>
      ) : (
        <LayerCard className="min-w-0 overflow-x-auto">
          <Table>
            <Table.Header>
              <Table.Row>
                <Table.Head>Worker</Table.Head>
                <Table.Head>Binding</Table.Head>
                <Table.Head>Outbound Worker</Table.Head>
                <Table.Head>Revision / traffic</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {rows.map((row) => (
                <Table.Row key={`${row.worker.id}:${row.revision}:${row.binding}`}>
                  <Table.Cell>
                    <Link className="text-kumo-link underline" to={`/workers/${row.worker.id}`}>
                      {row.worker.name}
                    </Link>
                  </Table.Cell>
                  <Table.Cell>{row.binding}</Table.Cell>
                  <Table.Cell>
                    {row.outbound ? (
                      <Link className="text-kumo-link underline" to={`/workers/${row.outbound}`}>
                        View outbound Worker
                      </Link>
                    ) : (
                      "None"
                    )}
                  </Table.Cell>
                  <Table.Cell>
                    {row.revision.slice(0, 8)} / {row.traffic}%
                  </Table.Cell>
                </Table.Row>
              ))}
              {!rows.length && (
                <Table.Row>
                  <Table.Cell colSpan={4}>
                    {loading
                      ? "Loading dispatchers…"
                      : error
                        ? "Dispatcher references unavailable."
                        : "No active dispatcher bindings reference this namespace."}
                  </Table.Cell>
                </Table.Row>
              )}
            </Table.Body>
          </Table>
        </LayerCard>
      )}
    </section>
  );
}
