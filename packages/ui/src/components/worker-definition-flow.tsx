import { Flow } from "@cloudflare/kumo";
import {
  Clock3,
  Database,
  DatabaseZap,
  FolderOpen,
  Globe2,
  KeyRound,
  ShieldCheck,
  Waypoints,
} from "lucide-react";
import { forwardRef, type ComponentPropsWithoutRef } from "react";
import type {
  Database as DatabaseResource,
  KVNamespace,
  Worker,
  WorkerDeployment,
} from "../app/types";
import { cn } from "../lib/utils";

type WorkerDefinitionFlowProps = {
  deployment?: WorkerDeployment;
  databases: DatabaseResource[];
  namespaces: KVNamespace[];
  onOpenBucket: (bucketID: string) => void;
  onOpenDatabase: (databaseID: string) => void;
  onOpenNamespace: (namespaceID: string) => void;
  worker: Worker;
};

type BindingItem = {
  binding: string;
  bucketID?: string;
  databaseID?: string;
  namespaceID?: string;
  subtitle: string;
  type: "asset" | "db" | "kv" | "object";
};

export function WorkerDefinitionFlow({
  databases,
  deployment,
  namespaces,
  onOpenBucket,
  onOpenDatabase,
  onOpenNamespace,
  worker,
}: WorkerDefinitionFlowProps) {
  const namespaceByID = new Map(namespaces.map((namespace) => [namespace.id, namespace]));
  const databaseByID = new Map(databases.map((database) => [database.id, database]));
  const protectedRoutes = worker.auth?.protected_routes ?? [];
  const crons = deployment?.triggers?.crons ?? [];
  const bindings = deployment?.bindings ?? worker.bindings ?? [];
  const items: BindingItem[] = bindings.map((binding) => {
    if (binding.kind === "asset")
      return {
        binding: binding.binding,
        subtitle: `${binding.asset_count ?? 0} static asset${binding.asset_count === 1 ? "" : "s"}`,
        type: "asset",
      };
    if (binding.kind === "object_storage_bucket")
      return {
        binding: binding.binding,
        bucketID: binding.bucket_id,
        subtitle: binding.bucket_name ?? binding.bucket_id ?? "bucket",
        type: "object",
      };
    if (binding.kind === "db")
      return {
        binding: binding.binding,
        databaseID: binding.database_id,
        subtitle:
          binding.database_name ??
          databaseByID.get(binding.database_id ?? "")?.name ??
          binding.database_id ??
          "database",
        type: "db",
      };
    return {
      binding: binding.binding,
      namespaceID: binding.namespace_id,
      subtitle:
        binding.namespace_name ??
        namespaceByID.get(binding.namespace_id ?? "")?.name ??
        binding.namespace_id ??
        "namespace",
      type: "kv",
    };
  });

  return (
    <Flow
      align="center"
      canvas={false}
      className="min-h-[330px] rounded-xl bg-kumo-base ring ring-kumo-hairline"
      orientation="horizontal"
      padding={{ x: 24, y: 48 }}
    >
      {crons.length ? (
        <Flow.Parallel>
          <Flow.List>
            <Flow.Node
              render={
                <DefinitionCard
                  eyebrow="Ingress"
                  icon={Globe2}
                  title={worker.hostname}
                  tone="graphite"
                />
              }
            />
            {protectedRoutes.length ? (
              <Flow.Node
                render={
                  <DefinitionCard
                    eyebrow="Middleware"
                    icon={ShieldCheck}
                    title={`Auth verify (${protectedRoutes.length})`}
                    tone="orange"
                  />
                }
              />
            ) : null}
          </Flow.List>
          <Flow.Node
            render={
              <DefinitionCard
                eyebrow="Trigger"
                icon={Clock3}
                title={crons.length === 1 ? crons[0] : `${crons.length} cron triggers`}
                tone="blue"
              />
            }
          />
        </Flow.Parallel>
      ) : (
        <>
          <Flow.Node
            render={
              <DefinitionCard
                eyebrow="Ingress"
                icon={Globe2}
                title={worker.hostname}
                tone="graphite"
              />
            }
          />
          {protectedRoutes.length ? (
            <Flow.Node
              render={
                <DefinitionCard
                  eyebrow="Middleware"
                  icon={ShieldCheck}
                  title={`Auth verify (${protectedRoutes.length})`}
                  tone="orange"
                />
              }
            />
          ) : null}
        </>
      )}
      <Flow.Node
        render={
          <DefinitionCard eyebrow="Runtime" icon={Waypoints} title={worker.name} tone="sage" />
        }
      />
      <Flow.Node
        render={
          <BindingsCard
            items={items}
            onOpenBucket={onOpenBucket}
            onOpenDatabase={onOpenDatabase}
            onOpenNamespace={onOpenNamespace}
          />
        }
      />
    </Flow>
  );
}

type DefinitionCardProps = ComponentPropsWithoutRef<"div"> & {
  eyebrow: string;
  icon: typeof Globe2;
  title: string;
  tone: "blue" | "graphite" | "orange" | "sage";
};

const DefinitionCard = forwardRef<HTMLDivElement, DefinitionCardProps>(function DefinitionCard(
  { eyebrow, icon: Icon, title, tone, className, ...props },
  ref,
) {
  const toneClass =
    tone === "orange"
      ? "bg-orange-50 text-orange-700 ring-orange-200"
      : tone === "sage"
        ? "bg-green-50 text-green-700 ring-green-200"
        : tone === "blue"
          ? "bg-blue-50 text-blue-700 ring-blue-200"
          : "bg-gray-50 text-gray-600 ring-gray-200";
  return (
    <div
      {...props}
      ref={ref}
      className={cn(
        "w-44 rounded-xl bg-kumo-base px-4 py-3 shadow-sm ring ring-kumo-hairline",
        className,
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="font-mono text-xs text-kumo-subtle">{eyebrow}</p>
          <h3 className="mt-1.5 truncate text-sm font-semibold text-kumo-default">{title}</h3>
        </div>
        <div
          className={cn(
            "flex size-9 shrink-0 items-center justify-center rounded-full ring",
            toneClass,
          )}
        >
          <Icon className="size-4" />
        </div>
      </div>
    </div>
  );
});

type BindingsCardProps = ComponentPropsWithoutRef<"div"> & {
  items: BindingItem[];
  onOpenBucket: (id: string) => void;
  onOpenDatabase: (id: string) => void;
  onOpenNamespace: (id: string) => void;
};

const BindingsCard = forwardRef<HTMLDivElement, BindingsCardProps>(function BindingsCard(
  { items, onOpenBucket, onOpenDatabase, onOpenNamespace, className, ...props },
  ref,
) {
  return (
    <div
      {...props}
      ref={ref}
      className={cn(
        "w-60 overflow-hidden rounded-xl bg-kumo-base shadow-sm ring ring-kumo-hairline",
        className,
      )}
    >
      <div className="p-2">
        <div className="flex items-center justify-between rounded-lg bg-kumo-overlay px-3 py-2 ring ring-kumo-hairline">
          <h3 className="text-sm font-semibold text-kumo-default">Bindings</h3>
          <span className="flex min-w-7 items-center justify-center rounded bg-kumo-contrast px-2 py-1 font-mono text-xs text-kumo-inverse">
            {items.length}
          </span>
        </div>
      </div>
      <div className="divide-y divide-kumo-hairline">
        {items.length ? (
          items.map((item) => (
            <BindingRow
              key={`${item.type}-${item.binding}-${item.namespaceID ?? item.databaseID ?? item.bucketID ?? "asset"}`}
              item={item}
              onOpenBucket={onOpenBucket}
              onOpenDatabase={onOpenDatabase}
              onOpenNamespace={onOpenNamespace}
            />
          ))
        ) : (
          <div className="px-5 py-4 text-sm text-kumo-subtle">No bindings attached</div>
        )}
      </div>
    </div>
  );
});

function BindingRow({
  item,
  onOpenBucket,
  onOpenDatabase,
  onOpenNamespace,
}: {
  item: BindingItem;
  onOpenBucket: (id: string) => void;
  onOpenDatabase: (id: string) => void;
  onOpenNamespace: (id: string) => void;
}) {
  const isKV = item.type === "kv";
  const isDB = item.type === "db";
  const isObject = item.type === "object";
  const Icon = isKV ? KeyRound : isDB ? Database : isObject ? DatabaseZap : FolderOpen;
  const color = isKV
    ? "text-green-700"
    : isDB
      ? "text-cyan-700"
      : isObject
        ? "text-blue-700"
        : "text-yellow-700";
  const open =
    isKV && item.namespaceID
      ? () => onOpenNamespace(item.namespaceID!)
      : isDB && item.databaseID
        ? () => onOpenDatabase(item.databaseID!)
        : isObject && item.bucketID
          ? () => onOpenBucket(item.bucketID!)
          : undefined;
  return (
    <div className="px-5 py-3">
      <div className="mb-1.5 flex items-center gap-2 text-xs text-kumo-subtle">
        <Icon className={cn("size-4", color)} />
        {isKV ? "KV" : isDB ? "Database" : isObject ? "Object storage" : "Assets"}
      </div>
      <div className="flex items-center gap-2 text-sm font-medium text-kumo-default">
        <span className="min-w-0 flex-1 truncate font-mono text-[0.9em]">{item.binding}</span>
        <span className="text-kumo-subtle">→</span>
        {open ? (
          <button
            className={cn(
              "min-w-0 flex-1 truncate rounded text-left underline underline-offset-4",
              color,
            )}
            onClick={open}
            type="button"
          >
            {item.subtitle}
          </button>
        ) : (
          <span className="min-w-0 flex-1 truncate text-kumo-subtle">{item.subtitle}</span>
        )}
      </div>
    </div>
  );
}
