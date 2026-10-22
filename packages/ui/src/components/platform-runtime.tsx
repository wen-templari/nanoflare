import type { components } from "@nanoflare/schema";

import { Banner, Field, LayerCard, Table, Text } from "@cloudflare/kumo";
import { useEffect, useState, type FormEvent } from "react";

import { apiClient, errorMessage } from "../app/api";
import { useAuth } from "../app/auth-context";
import { Button } from "./ui/button";
import { Dialog } from "./ui/dialog";

type DeploymentInput = components["schemas"]["DeployInput"];
type Capabilities = components["schemas"]["RuntimeCapabilities"];
type Publication = components["schemas"]["PublicationResult"];

export function DeployPlatformBundle({
  workerID,
  open,
  onClose,
  onSaved,
}: {
  workerID: string;
  open: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { activeOrgID } = useAuth();
  const [bundle, setBundle] = useState<DeploymentInput>();
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    setBundle(undefined);
    setError("");
  }, [workerID, activeOrgID]);
  async function choose(file?: File) {
    setError("");
    setBundle(undefined);
    if (!file) return;
    try {
      if (file.size > 10 * 1024 * 1024)
        throw new Error("Deployment bundles must be at most 10 MiB.");
      const value = JSON.parse(await file.text()) as DeploymentInput;
      if (
        !Array.isArray(value.files) ||
        !value.files.length ||
        typeof value.compatibility_date !== "string"
      )
        throw new Error(
          "Choose a complete Nanoflare deployment bundle with files and a compatibility date.",
        );
      setBundle({ ...value, idempotency_key: value.idempotency_key || crypto.randomUUID() });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not read the bundle");
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!bundle) return;
    setSaving(true);
    setError("");
    try {
      const result = await apiClient.POST(
        "/v1/organizations/{orgID}/workers/{workerID}/deployments",
        {
          params: { path: { orgID: activeOrgID, workerID } },
          body: bundle,
        },
      );
      if (result.error || !result.data)
        throw new Error(
          errorMessage(
            result.error,
            "Deployment failed. Retry this bundle to recover its publication status.",
          ),
        );
      setBundle(undefined);
      onSaved();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Deployment failed");
    } finally {
      setSaving(false);
    }
  }
  return (
    <Dialog
      open={open}
      onClose={() => {
        if (!saving) onClose();
      }}
      title="Deploy a bundle"
      description="Upload the complete code, variables, bindings and assets for this revision. Saved Worker secrets are used. Existing revisions remain available for rollback."
    >
      <form onSubmit={submit} className="grid gap-4 text-sm">
        {error && <Banner variant="error">{error}</Banner>}
        <Field label="Deployment bundle">
          <input
            aria-label="Deployment bundle"
            type="file"
            accept="application/json,.json"
            disabled={saving}
            onChange={(event) => void choose(event.target.files?.[0])}
            className="block w-full text-sm"
          />
        </Field>
        <Text variant="secondary">
          Export the bundle with <code className="text-[0.9em]">nanoflare platform bundle</code>, or
          use your platform’s deployment pipeline.
        </Text>
        {bundle && (
          <dl className="grid grid-cols-2 gap-2">
            <dt>Modules</dt>
            <dd>{bundle.files?.length ?? 0}</dd>
            <dt>Assets</dt>
            <dd>{bundle.assets?.length ?? 0}</dd>
            <dt>Compatibility date</dt>
            <dd>{bundle.compatibility_date}</dd>
            <dt>Entrypoint</dt>
            <dd>{bundle.entrypoint || bundle.files?.[0]?.path}</dd>
          </dl>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" type="button" disabled={saving} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={saving} disabled={!bundle || saving}>
            Deploy revision
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function PlatformRuntime({
  workerID,
  onPublished,
}: {
  workerID: string;
  onPublished?: () => void;
}) {
  const { activeOrgID, organizations } = useAuth();
  const canDeploy =
    organizations.find((org) => org.id === activeOrgID)?.scopes?.includes("deployments:write") ??
    false;
  const [capabilities, setCapabilities] = useState<Capabilities>();
  const [publications, setPublications] = useState<Publication[]>([]);
  const [error, setError] = useState("");
  const [revision, refresh] = useState(0);
  const [deployOpen, setDeployOpen] = useState(false);
  useEffect(() => {
    let current = true;
    setCapabilities(undefined);
    setPublications([]);
    setError("");
    const path = { orgID: activeOrgID, workerID };
    void Promise.all([
      apiClient.GET("/v1/organizations/{orgID}/workers/{workerID}/capabilities", {
        params: { path },
      }),
      apiClient.GET("/v1/organizations/{orgID}/workers/{workerID}/publications", {
        params: { path },
      }),
    ])
      .then(([cap, pub]) => {
        if (!current) return;
        if (cap.error || pub.error) {
          setError(errorMessage(cap.error || pub.error, "Runtime details unavailable"));
          return;
        }
        setCapabilities(cap.data);
        setPublications(pub.data ?? []);
      })
      .catch((err) => {
        if (current) setError(String(err));
      });
    return () => {
      current = false;
    };
  }, [activeOrgID, workerID, revision]);
  return (
    <div className="grid gap-6 text-sm">
      {error && <Banner variant="error">{error}</Banner>}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Text as="h2" variant="heading3">
          Runtime capabilities
        </Text>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => refresh((value) => value + 1)}>
            Refresh
          </Button>
          <Button disabled={!canDeploy} onClick={() => setDeployOpen(true)}>
            Deploy a bundle
          </Button>
        </div>
      </div>
      {capabilities && (
        <LayerCard className="px-5 py-4">
          <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-[200px_1fr]">
            <dt>Execution</dt>
            <dd>
              {capabilities.execution === "dynamic" ? "Private dynamic Worker" : "Static Worker"} ·{" "}
              {capabilities.enabled ? "Enabled" : "Disabled"}
            </dd>
            <dt>Isolation</dt>
            <dd>Trusted code in a shared runtime</dd>
            <dt>Public ingress</dt>
            <dd>{capabilities.public_ingress ? "Allowed" : "Private dispatch only"}</dd>
            <dt>CPU accounting and limits</dt>
            <dd>
              Accounting {capabilities.cpu_accounting ? "available" : "unavailable"} · Limits{" "}
              {capabilities.cpu_limit ? "available" : "unavailable"}
            </dd>
            <dt>Duration measurement</dt>
            <dd>{capabilities.duration_measurement.replace(/_/g, " ")}</dd>
            <dt>Outbound requests</dt>
            <dd>{capabilities.outbound.replace(/_/g, " ")}</dd>
            <dt>Cache API / raw TCP</dt>
            <dd>
              {capabilities.cache_api.replace(/_/g, " ")} /{" "}
              {capabilities.raw_tcp.replace(/_/g, " ")}
            </dd>
            {capabilities.execution === "dynamic" && (
              <>
                <dt>Invocation budgets</dt>
                <dd>
                  {capabilities.limits.wall_ms! / 1000} seconds wall time ·{" "}
                  {capabilities.limits.subrequests} shared effects · depth{" "}
                  {capabilities.limits.depth}
                </dd>
                <dt>Background authority</dt>
                <dd>
                  At most {capabilities.limits.background_ms! / 1000} seconds after response
                  completion
                </dd>
              </>
            )}
            {capabilities.dispatch_counters && (
              <>
                <dt>Dispatches since process start</dt>
                <dd>
                  {capabilities.dispatch_counters.invocations} admitted ·{" "}
                  {capabilities.dispatch_counters.completed} completed ·{" "}
                  {capabilities.dispatch_counters.canceled} canceled
                </dd>
              </>
            )}
            <dt>Engine version</dt>
            <dd>
              {capabilities.engine_version === "unreported"
                ? "Not reported by the runner"
                : capabilities.engine_version}
            </dd>
          </dl>
        </LayerCard>
      )}
      <div className="grid gap-3">
        <Text as="h2" variant="heading3">
          Publication history
        </Text>
        <Text variant="secondary">
          Published configuration and verified runtime readiness are separate. Dynamic revisions
          load when dispatched.
        </Text>
        <LayerCard className="overflow-x-auto">
          <Table>
            <Table.Header>
              <Table.Row>
                <Table.Head>Revision</Table.Head>
                <Table.Head>Stage</Table.Head>
                <Table.Head>Runtime readiness</Table.Head>
                <Table.Head>Updated</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {publications.map((item) => (
                <Table.Row key={item.idempotency_key}>
                  <Table.Cell>
                    <code className="text-[0.9em]">{item.deployment_id}</code>
                  </Table.Cell>
                  <Table.Cell>{item.stage.replace(/_/g, " ")}</Table.Cell>
                  <Table.Cell>
                    {item.runtime_ready_verified ? "Verified" : "Not verified"}
                  </Table.Cell>
                  <Table.Cell>{new Date(item.updated_at).toLocaleString()}</Table.Cell>
                </Table.Row>
              ))}
              {!publications.length && (
                <Table.Row>
                  <Table.Cell colSpan={4}>No publication records yet.</Table.Cell>
                </Table.Row>
              )}
            </Table.Body>
          </Table>
        </LayerCard>
      </div>
      <DeployPlatformBundle
        workerID={workerID}
        open={deployOpen}
        onClose={() => setDeployOpen(false)}
        onSaved={() => {
          refresh((value) => value + 1);
          onPublished?.();
        }}
      />
    </div>
  );
}
