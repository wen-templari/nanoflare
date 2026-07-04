import type { ReactNode } from "react";
import { Activity, MoreHorizontal } from "lucide-react";
import { cn } from "../../lib/utils";

export function PageHeading({ eyebrow, title, copy, actions }: { eyebrow: string; title: string; copy: string; actions?: ReactNode }) {
  return (
    <div className="animate-rise mb-7 flex flex-col justify-between gap-4 md:flex-row md:items-end">
      <div>
        <p className="font-mono text-[10px] uppercase tracking-[0.2em] text-[#d75a41]">{eyebrow}</p>
        <h1 className="font-display mt-1 text-4xl tracking-[-0.04em] text-[#26332f] md:text-5xl">{title}</h1>
        <p className="mt-2 max-w-xl text-sm leading-6 text-[#7a8079]">{copy}</p>
      </div>
      {actions}
    </div>
  );
}

export function Panel({ title, eyebrow, children, flush = false }: { title: string; eyebrow: string; children: ReactNode; flush?: boolean }) {
  return (
    <section className="paper-panel animate-rise overflow-hidden rounded-xl border border-[#dcd6ca] bg-[#fbf9f3]/85">
      <header className="flex items-center justify-between border-b border-[#e7e1d6] px-5 py-4">
        <div>
          <p className="font-mono text-[9px] uppercase tracking-[0.18em] text-[#d35c45]">{eyebrow}</p>
          <h2 className="mt-1 text-sm font-extrabold">{title}</h2>
        </div>
        <MoreHorizontal className="size-4 text-[#a1a49d]" />
      </header>
      <div className={flush ? "" : "p-5"}>{children}</div>
    </section>
  );
}

export function Event({ icon, text, time }: { icon: ReactNode; text: string; time: string }) {
  return (
    <div className="flex items-center gap-3 border-b border-[#ece7dc] py-3 last:border-0 [&_svg]:size-3.5 [&_svg]:text-[#d65c44]">
      <div className="grid size-8 place-items-center rounded-full bg-[#f3e5df]">{icon}</div>
      <p className="flex-1 text-xs font-bold">{text}</p>
      <span className="font-mono text-[9px] text-[#a1a49e]">{time}</span>
    </div>
  );
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block"><span className="mb-1.5 block font-mono text-[10px] uppercase tracking-[0.14em] text-[#7e847d]">{label}</span>{children}</label>;
}

export function WorkerDetailEmpty({ icon, title, copy }: { icon: ReactNode; title: string; copy: string }) {
  return <div className="grid min-h-[510px] place-items-center bg-white/30 text-center"><div className="[&_svg]:mx-auto [&_svg]:size-5 [&_svg]:text-[#b7b4ac]">{icon}<p className="mt-3 text-xs font-extrabold text-[#777e78]">{title}</p><p className="mt-1 font-mono text-[9px] uppercase tracking-[0.08em] text-[#a1a49e]">{copy}</p></div></div>;
}

export function EmptyMetrics() {
  return <div className="grid h-52 place-items-center rounded-lg border border-dashed border-[#d8d2c7] bg-white/30 text-center"><div><Activity className="mx-auto size-5 text-[#b7b4ac]" /><p className="mt-3 text-xs font-extrabold text-[#777e78]">No traffic samples yet</p><p className="mt-1 font-mono text-[9px] uppercase tracking-[0.08em] text-[#a1a49e]">Start the stack or send a request through Traefik</p></div></div>;
}

export function StatusCodeMix({ values }: { values: { code: string; value: number }[] }) {
  const total = values.reduce((sum, { value }) => sum + value, 0);
  if (!values.length) return <EmptyMetrics />;

  return (
    <div className="space-y-4">
      {values.map(({ code, value }) => (
        <div key={code}>
          <div className="mb-1.5 flex justify-between font-mono text-[10px]">
            <span className="font-bold text-[#58645f]">HTTP {code}</span>
            <span className="text-[#989c96]">{value.toFixed(2)}/s</span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-[#e6e3db]">
            <div
              className={cn("h-full rounded-full", code.startsWith("5") ? "bg-[#d75a41]" : code.startsWith("4") ? "bg-[#c89247]" : "bg-[#6d9c79]")}
              style={{ width: `${total ? Math.max((value / total) * 100, 2) : 0}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}
