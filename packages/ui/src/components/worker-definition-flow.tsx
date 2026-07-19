import "@xyflow/react/dist/style.css"

import Dagre from "@dagrejs/dagre"
import type { Edge, Node, NodeProps } from "@xyflow/react"
import {
  Background,
  Handle,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useEdgesState,
  useNodesInitialized,
  useNodesState,
  useReactFlow,
} from "@xyflow/react"
import { Clock3, Database, DatabaseZap, FolderOpen, Globe2, KeyRound, ShieldCheck, Waypoints } from "lucide-react"
import { createContext, useContext, useEffect, useMemo, useRef } from "react"
import type { Database as DatabaseResource, KVNamespace, Worker, WorkerDeployment } from "../app/types"
import { cn } from "../lib/utils"

type WorkerDefinitionFlowProps = {
  deployment?: WorkerDeployment
  databases: DatabaseResource[]
  namespaces: KVNamespace[]
  onOpenBucket: (bucketID: string) => void
  onOpenDatabase: (databaseID: string) => void
  onOpenNamespace: (namespaceID: string) => void
  worker: Worker
}

type DefinitionNodeData = {
  eyebrow: string
  icon: "domain" | "auth" | "trigger" | "worker"
  title: string
  tone?: "blue" | "graphite" | "orange" | "sage"
}

type BindingItem = {
  binding: string
  bucketID?: string
  databaseID?: string
  namespaceID?: string
  subtitle: string
  type: "asset" | "db" | "kv" | "object"
}

type BindingsNodeData = {
  items: BindingItem[]
  title: string
}

type FlowNodeData = DefinitionNodeData | BindingsNodeData
type FlowNode = Node<FlowNodeData>

const nodeTypes = {
  bindings: BindingsNode,
  definition: DefinitionNode,
}

const NamespaceNavigationContext = createContext<((namespaceID: string) => void) | null>(null)
const DatabaseNavigationContext = createContext<((databaseID: string) => void) | null>(null)
const BucketNavigationContext = createContext<((bucketID: string) => void) | null>(null)

const getLayoutedElements = (nodes: FlowNode[], edges: Edge[], direction: "LR" | "TB") => {
  const g = new Dagre.graphlib.Graph().setDefaultEdgeLabel(() => ({}))
  g.setGraph({ nodesep: 56, rankdir: direction, ranksep: 96 })

  edges.forEach((edge) => g.setEdge(edge.source, edge.target))
  nodes.forEach((node) =>
    g.setNode(node.id, {
      ...node,
      height: node.measured?.height ?? 0,
      width: node.measured?.width ?? 0,
    }),
  )

  Dagre.layout(g)

  return {
    edges,
    nodes: nodes.map((node) => {
      const position = g.node(node.id)
      const x = position.x - (node.measured?.width ?? 0) / 2
      const y = position.y - (node.measured?.height ?? 0) / 2
      return { ...node, position: { x, y } }
    }),
  }
}

export function WorkerDefinitionFlow(props: WorkerDefinitionFlowProps) {
  return (
    <NamespaceNavigationContext.Provider value={props.onOpenNamespace}>
      <DatabaseNavigationContext.Provider value={props.onOpenDatabase}>
        <BucketNavigationContext.Provider value={props.onOpenBucket}>
          <ReactFlowProvider>
            <LayoutedWorkerDefinitionFlow {...props} />
          </ReactFlowProvider>
        </BucketNavigationContext.Provider>
      </DatabaseNavigationContext.Provider>
    </NamespaceNavigationContext.Provider>
  )
}

function LayoutedWorkerDefinitionFlow({ databases, deployment, namespaces, worker }: WorkerDefinitionFlowProps) {
  const { fitView } = useReactFlow()
  const nodesInitialized = useNodesInitialized()
  const containerRef = useRef<HTMLDivElement>(null)
  const initialGraph = useMemo(() => buildGraph(worker, deployment, namespaces, databases), [databases, deployment, namespaces, worker])
  const graphKey = useMemo(
    () => JSON.stringify({
      bindings: deployment?.bindings ?? worker.bindings ?? [],
      hostname: worker.hostname,
      protectedRoutes: worker.auth?.protected_routes ?? [],
      triggers: deployment?.triggers?.crons ?? [],
      workerName: worker.name,
    }),
    [deployment?.bindings, deployment?.triggers?.crons, worker.auth?.protected_routes, worker.bindings, worker.hostname, worker.name],
  )
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>(initialGraph.nodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialGraph.edges)

  useEffect(() => {
    setNodes(initialGraph.nodes)
    setEdges(initialGraph.edges)
  }, [graphKey, initialGraph.edges, initialGraph.nodes, setEdges, setNodes])

  useEffect(() => {
    if (!nodesInitialized) return
    const layouted = getLayoutedElements(nodes, edges, "LR")
    setNodes([...layouted.nodes])
    setEdges([...layouted.edges])
    window.requestAnimationFrame(() => void fitView({ duration: 250, padding: 0.16 }))
    // We only want to relayout when measured nodes become ready or the graph shape changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodesInitialized, graphKey, fitView, setEdges, setNodes])

  useEffect(() => {
    if (!nodesInitialized || !containerRef.current) return
    const observer = new ResizeObserver(() => {
      window.requestAnimationFrame(() => void fitView({ duration: 250, maxZoom: 1, padding: 0.2 }))
    })
    observer.observe(containerRef.current)
    return () => observer.disconnect()
  }, [fitView, nodesInitialized])

  return (
    <div ref={containerRef} className="h-96 rounded-xl border border-gray-200 bg-white">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        fitView
        fitViewOptions={{ maxZoom: 1, padding: 0.2 }}
        nodesConnectable={false}
        nodesFocusable={false}
        nodesDraggable={false}
        elementsSelectable={false}
        maxZoom={1}
        minZoom={0.2}
        panOnScroll={false}
        zoomOnDoubleClick={false}
        zoomOnPinch={false}
        zoomOnScroll={false}
        panOnDrag={false}
        preventScrolling={false}
        proOptions={{ hideAttribution: true }}
      >
        <Background gap={20} size={1} />
      </ReactFlow>
    </div>
  )
}

function buildGraph(worker: Worker, deployment: WorkerDefinitionFlowProps["deployment"], namespaces: KVNamespace[], databases: DatabaseResource[]) {
  const namespaceByID = new Map(namespaces.map((namespace) => [namespace.id, namespace]))
  const databaseByID = new Map(databases.map((database) => [database.id, database]))
  const protectedRoutes = worker.auth?.protected_routes ?? []
  const crons = deployment?.triggers?.crons ?? []
  const bindings = deployment?.bindings ?? worker.bindings ?? []
  const bindingItems: BindingItem[] = bindings.map((binding) => {
    if (binding.kind === "asset") {
      return {
        binding: binding.binding,
        subtitle: `${binding.asset_count ?? 0} static asset${binding.asset_count === 1 ? "" : "s"}`,
        type: "asset" as const,
      }
    }
    if (binding.kind === "object_storage_bucket") {
      return {
        binding: binding.binding,
        bucketID: binding.bucket_id,
        subtitle: binding.bucket_name ?? binding.bucket_id ?? "bucket",
        type: "object" as const,
      }
    }
    if (binding.kind === "db") {
      return {
        binding: binding.binding,
        databaseID: binding.database_id,
        subtitle: binding.database_name ?? databaseByID.get(binding.database_id ?? "")?.name ?? binding.database_id ?? "database",
        type: "db" as const,
      }
    }
    return {
      binding: binding.binding,
      namespaceID: binding.namespace_id,
      subtitle: binding.namespace_name ?? namespaceByID.get(binding.namespace_id ?? "")?.name ?? binding.namespace_id ?? "namespace",
      type: "kv" as const,
    }
  })

  const nodes: FlowNode[] = [
    {
      data: {
        eyebrow: "Ingress",
        icon: "domain",
        title: worker.hostname,
        tone: "graphite",
      },
      id: "domain",
      position: { x: 0, y: 0 },
      type: "definition",
    },
    ...(protectedRoutes.length
      ? [{
        data: {
          eyebrow: "Middleware",
          icon: "auth",
          title: `Auth verify (${protectedRoutes.length})`,
          tone: "orange",
        },
        id: "auth",
        position: { x: 0, y: 0 },
        type: "definition",
      } satisfies Node<FlowNodeData>]
      : []),
    ...(crons.length
      ? [{
        data: {
          eyebrow: "Trigger",
          icon: "trigger",
          title: crons.length === 1 ? crons[0] : `${crons.length} cron triggers`,
          tone: "blue",
        },
        id: "triggers",
        position: { x: 0, y: 0 },
        type: "definition",
      } satisfies Node<FlowNodeData>]
      : []),
    {
      data: {
        eyebrow: "Runtime",
        icon: "worker",
        title: worker.name,
        tone: "sage",
      },
      id: "worker",
      position: { x: 0, y: 0 },
      type: "definition",
    },
    {
      data: {
        items: bindingItems,
        title: "Bindings",
      },
      id: "bindings",
      position: { x: 0, y: 0 },
      type: "bindings",
    },
  ]

  const edges: Edge[] = [
    {
      animated: true,
      id: "domain-edge",
      source: "domain",
      target: protectedRoutes.length ? "auth" : "worker",
      type: "smoothstep",
    },
    ...(protectedRoutes.length
      ? [{
        animated: true,
        id: "auth-edge",
        source: "auth",
        target: "worker",
        type: "smoothstep",
      } satisfies Edge]
      : []),
    ...(crons.length
      ? [{
        animated: true,
        id: "triggers-edge",
        source: "triggers",
        target: "worker",
        type: "smoothstep",
      } satisfies Edge]
      : []),
    {
      animated: true,
      id: "worker-bindings-edge",
      source: "worker",
      target: "bindings",
      type: "smoothstep",
    },
  ]

  return { edges, nodes }
}

function DefinitionNode({ data }: NodeProps<Node<DefinitionNodeData>>) {
  const Icon = data.icon === "domain" ? Globe2 : data.icon === "auth" ? ShieldCheck : data.icon === "trigger" ? Clock3 : Waypoints

  return (
    <div
      className={cn(
        "nodrag nopan min-w-56 rounded-xl border bg-white p-4",
        data.tone === "orange" ? "border-orange-200" : data.tone === "sage" ? "border-green-200" : data.tone === "blue" ? "border-blue-200" : "border-gray-200",
      )}
    >
      <Handle type="target" position={Position.Left} className="!h-2.5 !w-2.5 !border-2 !border-white !bg-gray-300" />
      <Handle type="source" position={Position.Right} className="!h-2.5 !w-2.5 !border-2 !border-white !bg-green-500" />
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="font-mono text-sm font-medium text-gray-500">{data.eyebrow}</p>
          <h3 className="mt-2 font-semibold text-gray-900">{data.title}</h3>
        </div>
        <div className={cn("flex size-9 items-center justify-center rounded-full border", data.tone === "orange" ? "border-orange-200 bg-orange-50 text-orange-600" : data.tone === "sage" ? "border-green-200 bg-green-50 text-green-700" : data.tone === "blue" ? "border-blue-200 bg-blue-50 text-blue-700" : "border-gray-200 bg-gray-50 text-gray-600")}>
          <Icon className="size-4" />
        </div>
      </div>
    </div>
  )
}

function BindingsNode({ data }: NodeProps<Node<BindingsNodeData>>) {
  return (
    <div className="nodrag nopan pointer-events-auto w-96 overflow-hidden rounded-xl border border-gray-200 bg-white">
      <Handle type="target" position={Position.Left} className="!h-2.5 !w-2.5 !border-2 !border-white !bg-gray-300" />
      <div className="p-2">
        <div className="flex items-center justify-between rounded-lg border border-gray-200 bg-white px-4 py-3">
          <div className="flex items-center gap-3">
            <h3 className="text-sm font-semibold text-gray-900">{data.title}</h3>
            <span className="flex min-w-8 items-center justify-center rounded-md bg-gray-700 px-2 py-1 font-mono text-xs font-bold text-white">{data.items.length}</span>
          </div>
        </div>
      </div>

      <div className="divide-y divide-gray-200">
        {data.items.length ? data.items.map((item) => (
          <BindingRow key={`${item.type}-${item.binding}-${item.namespaceID ?? item.databaseID ?? item.bucketID ?? "asset"}`} item={item} />
        )) : (
          <div className="px-5 py-4 text-sm">
            <p className="text-gray-600">No bindings attached</p>
            <p className="mt-2 font-mono text-xs text-gray-500">Deploy assets, KV namespaces, databases, or object buckets to populate this section.</p>
          </div>
        )}
      </div>
    </div>
  )
}

function BindingRow({ item }: { item: BindingItem }) {
  const openNamespace = useContext(NamespaceNavigationContext)
  const openDatabase = useContext(DatabaseNavigationContext)
  const openBucket = useContext(BucketNavigationContext)
  const isKV = item.type === "kv"
  const isDB = item.type === "db"
  const isObject = item.type === "object"
  const label = isKV ? "KV" : isDB ? "Database" : isObject ? "Object storage" : "Assets"

  return (
    <div className="pointer-events-auto px-5 py-3">
      <div className="mb-2 flex items-center gap-2">
        {isKV ? <KeyRound className="size-4 text-green-700" /> : isDB ? <Database className="size-4 text-cyan-700" /> : isObject ? <DatabaseZap className="size-4 text-blue-700" /> : <FolderOpen className="size-4 text-yellow-700" />}
        <p className="text-xs font-semibold uppercase tracking-wide text-gray-500">{label}</p>
      </div>
      <div className="flex items-center gap-2 font-semibold text-gray-900">
        <span className="min-w-0 flex-1 truncate font-mono">{item.binding}</span>
        <span className="text-gray-400">-&gt;</span>
        {isKV && item.namespaceID ? (
          <button
            type="button"
            onPointerDown={(event) => {
              event.preventDefault()
              event.stopPropagation()
            }}
            onMouseDown={(event) => {
              event.preventDefault()
              event.stopPropagation()
            }}
            onClick={(event) => {
              event.preventDefault()
              event.stopPropagation()
              openNamespace?.(item.namespaceID!)
            }}
            className="nodrag nopan pointer-events-auto relative z-10 min-w-0 flex-1 truncate rounded text-left text-green-700 underline underline-offset-4 transition hover:text-green-900"
          >
            {item.subtitle}
          </button>
        ) : isDB && item.databaseID ? (
          <button
            type="button"
            onPointerDown={(event) => {
              event.preventDefault()
              event.stopPropagation()
            }}
            onMouseDown={(event) => {
              event.preventDefault()
              event.stopPropagation()
            }}
            onClick={(event) => {
              event.preventDefault()
              event.stopPropagation()
              openDatabase?.(item.databaseID!)
            }}
            className="nodrag nopan pointer-events-auto relative z-10 min-w-0 flex-1 truncate rounded text-left text-cyan-700 underline underline-offset-4 transition hover:text-cyan-900"
          >
            {item.subtitle}
          </button>
        ) : isObject && item.bucketID ? (
          <button
            type="button"
            onPointerDown={(event) => {
              event.preventDefault()
              event.stopPropagation()
            }}
            onMouseDown={(event) => {
              event.preventDefault()
              event.stopPropagation()
            }}
            onClick={(event) => {
              event.preventDefault()
              event.stopPropagation()
              openBucket?.(item.bucketID!)
            }}
            className="nodrag nopan pointer-events-auto relative z-10 min-w-0 flex-1 truncate rounded text-left text-blue-700 underline underline-offset-4 transition hover:text-blue-900"
          >
            {item.subtitle}
          </button>
        ) : (
          <span className="min-w-0 flex-1 truncate text-gray-700">{item.subtitle}</span>
        )}
      </div>
    </div>
  )
}
