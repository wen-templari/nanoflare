import { Boxes, Check, ChevronDown, CircleGauge, KeyRound, Settings, Waypoints } from "lucide-react"
import { NavLink, Outlet, useLocation } from "react-router-dom"
import { useWorkspace } from "../../app/workspace-context"
import { CreateKVNamespaceDialog } from "../dialogs/create-kv-namespace-dialog"
import { CreateWorkerDialog } from "../dialogs/create-worker-dialog"
import { cn } from "../../lib/utils"

const navItems = [
  { href: "/", match: "/", label: "Overview", icon: CircleGauge },
  { href: "/workers", match: "/workers", label: "Workers", icon: Waypoints },
  { href: "/kv", match: "/kv", label: "KV", icon: KeyRound },
]

export function ConsoleLayout() {
  const location = useLocation()
  const {
    workers,
    setWorkers,
    namespaces,
    setNamespaces,
    apiConnected,
    workerDialogOpen,
    namespaceDialogOpen,
    openWorkerDialog,
    closeWorkerDialog,
    openNamespaceDialog,
    closeNamespaceDialog,
    toast,
    notify,
  } = useWorkspace()

  const activeSection = navItems.find(({ match }) => location.pathname === match || (match !== "/" && location.pathname.startsWith(match)))?.label.toLowerCase() ?? "overview"

  return (
    <div className="console-grid min-h-screen">
      <aside className="nav-noise fixed inset-y-0 left-0 z-30 hidden w-60 flex-col bg-[#1c2926] text-[#e7e4da] lg:flex">
        <div className="flex h-20 items-center border-b border-white/10 px-5">
          <div className="grid size-9 place-items-center rounded-lg bg-[#e25b3f] text-white shadow-lg shadow-black/15"><Boxes className="size-5" /></div>
          <div className="ml-3">
            <p className=" text-xl leading-none">nanoflare</p>
            <p className="mt-1 font-mono text-[9px]   text-[#9eb0a8]">control plane</p>
          </div>
        </div>
        <nav className="flex-1 space-y-1 px-3 py-5">
          <p className="px-3 pb-2 font-mono text-[9px]   text-[#83938e]">Workspace</p>
          {navItems.map(({ href, match, label, icon: Icon }) => {
            const active = location.pathname === match || (match !== "/" && location.pathname.startsWith(match))

            return (
              <NavLink
                key={href}
                to={href}
                className={cn(
                  "flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-[13px] font-semibold transition",
                  active ? "bg-white/11 text-white shadow-sm" : "text-[#aebdb7] hover:bg-white/6 hover:text-white",
                )}
              >
                <Icon className={cn("size-4", active && "text-[#ee765c]")} />
                {label}
              </NavLink>
            )
          })}
        </nav>
        <button className="flex items-center gap-3 border-t border-white/10 px-5 py-4 text-xs font-semibold text-[#aebdb7] hover:text-white"><Settings className="size-4" />Settings</button>
      </aside>

      <main className="pb-20 lg:pb-0 lg:pl-60">
        <header className="sticky top-0 z-20 flex h-16 items-center justify-between border-b border-[#ded9ce] bg-[#f4f0e7]/85 px-4 backdrop-blur-md md:px-8">
          <div>
            <p className="text-sm  text-[#90958e]">
              <span className="text-[#cf563d] capitalize">{activeSection}</span>
            </p>
          </div>
          <div className="flex items-center gap-3">
            {apiConnected && (
              <div className="hidden items-center gap-2 rounded-full border border-[#bfd4c4] bg-white/50 px-3 py-1.5 text-[11px] font-bold text-[#397046] sm:flex">
                <span className="size-1.5 rounded-full bg-[#52a46a]" />
                WORKER API CONNECTED
              </div>
            )}
            <button className="flex items-center gap-2 rounded-full bg-[#26332f] py-1.5 pl-1.5 pr-3 text-xs font-bold text-white">
              <span className="grid size-6 place-items-center rounded-full bg-[#e25b3f] text-[10px]">CL</span> clas <ChevronDown className="size-3" />
            </button>
          </div>
        </header>

        <div className="mx-auto max-w-7xl p-4 md:p-8">
          <Outlet />
        </div>
      </main>

      <nav className="fixed inset-x-3 bottom-3 z-30 flex justify-around rounded-xl border border-white/10 bg-[#1c2926]/95 p-1.5 text-[#aebdb7] shadow-2xl backdrop-blur-md lg:hidden">
        {navItems.map(({ href, match, label, icon: Icon }) => {
          const active = location.pathname === match || (match !== "/" && location.pathname.startsWith(match))

          return (
            <NavLink
              key={href}
              to={href}
              className={cn("flex min-w-16 flex-col items-center gap-1 rounded-lg px-2 py-2 font-mono text-[8px]   transition", active && "bg-white/10 text-white")}
            >
              <Icon className={cn("size-4", active && "text-[#ee765c]")} />
              {label}
            </NavLink>
          )
        })}
      </nav>

      <CreateWorkerDialog open={workerDialogOpen} onClose={closeWorkerDialog} workers={workers} setWorkers={(nextWorkers) => setWorkers(nextWorkers)} notify={notify} apiConnected={apiConnected} />
      <CreateKVNamespaceDialog open={namespaceDialogOpen} onClose={closeNamespaceDialog} namespaces={namespaces} setNamespaces={setNamespaces} notify={notify} apiConnected={apiConnected} />

      {toast && (
        <div className="fixed bottom-5 right-5 z-[60] flex items-center gap-2 rounded-lg bg-[#26332f] px-4 py-3 text-xs font-bold text-white shadow-xl">
          <Check className="size-4 text-[#8dc99b]" />
          {toast}
        </div>
      )}
    </div>
  )
}
