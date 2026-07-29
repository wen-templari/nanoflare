import { LayerCard, Text } from "@cloudflare/kumo";
import {
  ArrowUpRight,
  DatabaseZap,
  KeyRound,
  Waypoints,
} from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../app/auth-context";
import { useWorkspace } from "../app/workspace-context";
import { PageHeading } from "../components/shared/primitives";

export function OverviewPage() {
  const navigate = useNavigate();
  const { userEmail } = useAuth();
  const { workers, namespaces, objectStorageBuckets } = useWorkspace();
  const userName = displayNameFromEmail(userEmail);
  const kvBindings = workers.reduce(
    (count, worker) =>
      count + (worker.bindings?.filter((binding) => binding.kind === "kv").length ?? 0),
    0,
  );
  const objectBindings = workers.reduce(
    (count, worker) =>
      count +
      (worker.bindings?.filter((binding) => binding.kind === "object_storage_bucket").length ?? 0),
    0,
  );
  const stats = [
    {
      label: "Workers",
      value: workers.length,
      note: `${workers.filter((worker) => worker.status === "live").length} live · ${workers.filter((worker) => worker.status === "draft").length} draft`,
      icon: Waypoints,
      href: "/workers",
    },
    {
      label: "KV",
      value: namespaces.length,
      note: `${kvBindings} active bindings across workers`,
      icon: KeyRound,
      href: "/kv",
    },
    {
      label: "Object storage",
      value: objectStorageBuckets.length,
      note: `${objectBindings} active bucket bindings`,
      icon: DatabaseZap,
      href: "/object-storage",
    },
  ];

  return (
    <>
      <PageHeading
        eyebrow="Sunday, 31 May"
        title={`Good afternoon, ${userName}.`}
        copy="Your private runtime is steady. Here is the shape of your workspace today."
      />
      <div className="grid gap-4 md:grid-cols-3">
        {stats.map(({ label, value, note, icon: Icon, href }, index) => (
          <button
            className="text-left"
            key={label}
            onClick={() => navigate(href)}
            style={{ animationDelay: `${index * 80}ms` }}
            type="button"
          >
            <LayerCard className="h-full px-5 py-4">
              <div className="flex items-center justify-between gap-3">
                <span className="grid size-9 place-items-center rounded-lg bg-kumo-info/15 text-kumo-info">
                  <Icon size={18} />
                </span>
                <ArrowUpRight size={16} />
              </div>
              <Text as="p" DANGEROUS_className="mt-6" variant="heading2">
                {value}
              </Text>
              <Text as="p" bold size="sm">
                {label}
              </Text>
              <Text as="p" size="xs" variant="secondary">
                {note}
              </Text>
            </LayerCard>
          </button>
        ))}
      </div>
    </>
  );
}

function displayNameFromEmail(email: string) {
  const localPart = email.split("@", 1)[0].trim();
  const firstName = localPart.split(/[._+-]/, 1)[0];

  return firstName ? firstName.charAt(0).toUpperCase() + firstName.slice(1) : "there";
}
