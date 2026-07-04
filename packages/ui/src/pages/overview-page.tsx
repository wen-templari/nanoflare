import { Archive, ArrowUpRight, CloudUpload, Code2, KeyRound, Waypoints } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useWorkspace } from "../app/workspace-context";
import { Event, PageHeading, Panel } from "../components/shared/primitives";

export function OverviewPage() {
  const navigate = useNavigate();
  const { workers, namespaces } = useWorkspace();
  const bindings = workers.reduce((count, worker) => count + (worker.kv_bindings?.length ?? 0), 0);
  const stats = [
    { label: "Workers", value: workers.length, note: `${workers.filter((worker) => worker.status === "live").length} live · ${workers.filter((worker) => worker.status === "draft").length} draft`, icon: Waypoints, href: "/workers" },
    { label: "KV", value: namespaces.length, note: `${bindings} active bindings across workers`, icon: KeyRound, href: "/kv" },
  ];

  return (
    <>
      <PageHeading eyebrow="Sunday, 31 May" title="Good afternoon, Clas." copy="Your private runtime is steady. Here is the shape of your workspace today." />
      <div className="grid gap-4 md:grid-cols-2">
        {stats.map(({ label, value, note, icon: Icon, href }, index) => (
          <button key={label} onClick={() => navigate(href)} style={{ animationDelay: `${index * 80}ms` }} className="paper-panel animate-rise group rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85 p-5 text-left transition hover:-translate-y-0.5 hover:border-[#c7c0b4]">
            <div className="flex justify-between"><Icon className="size-5 text-[#d75a41]" /><ArrowUpRight className="size-4 text-[#b8b7b0] transition group-hover:text-[#d75a41]" /></div>
            <p className="mt-8 font-display text-5xl tracking-[-0.06em]">{value}</p>
            <p className="mt-2 text-sm font-extrabold">{label}</p><p className="mt-1 font-mono text-[10px] text-[#91958e]">{note}</p>
          </button>
        ))}
      </div>
      <div className="mt-6 grid gap-6 lg:grid-cols-[1.5fr_1fr]">
        <Panel title="Runtime activity" eyebrow="Last 24 hours">
          <div className="flex h-52 items-end gap-2 px-1 pt-7">
            {[35, 44, 37, 58, 65, 52, 76, 68, 88, 72, 82, 96, 77, 64, 73, 56, 61, 49, 66, 72, 60, 52, 44, 59].map((height, index) => <div key={index} className="group relative flex-1 rounded-t bg-[#d7ded8] transition hover:bg-[#e25b3f]" style={{ height: `${height}%` }} />)}
          </div>
          <div className="mt-3 flex justify-between font-mono text-[9px] text-[#9ba09a]"><span>12 AM</span><span>6 AM</span><span>12 PM</span><span>NOW</span></div>
        </Panel>
        <Panel title="Recent events" eyebrow="Live log">
          <Event icon={<CloudUpload />} text="worker bundle deployed" time="34m" />
          <Event icon={<KeyRound />} text="env.KV binding refreshed" time="2h" />
          <Event icon={<Code2 />} text="billing-sync deployed" time="5h" />
          <Event icon={<Archive />} text="previous generation retired" time="8h" />
        </Panel>
      </div>
    </>
  );
}
