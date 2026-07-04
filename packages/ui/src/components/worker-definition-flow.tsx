import "@xyflow/react/dist/style.css";

import Dagre from "@dagrejs/dagre";
import type { Edge, Node, NodeProps } from "@xyflow/react";
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
} from "@xyflow/react";
import { DatabaseZap, FolderOpen, Globe2, KeyRound, ShieldCheck, Waypoints } from "lucide-react";
import { createContext, useContext, useEffect, useMemo, useRef } from "react";
import type { KVNamespace, Worker, WorkerDeployment } from "../app/types";
import { cn } from "../lib/utils";
import { Badge } from "./ui/badge";

type WorkerDefinitionFlowProps = {
  deployment?: WorkerDeployment;
  namespaces: KVNamespace[];
  onOpenBucket: (bucketID: string) => void;
  onOpenNamespace: (namespaceID: string) => void;
  worker: Worker;
};

type DefinitionNodeData = {
  content?: string;
  eyebrow: string;
  icon: "domain" | "auth" | "worker";
  title: string;
  tone?: "graphite" | "orange" | "sage";
};

type BindingItem = {
  binding: string;
  bucketID?: string;
  namespaceID?: string;
  subtitle: string;
  type: "asset" | "kv" | "object";
};

type BindingsNodeData = {
  items: BindingItem[];
  title: string;
};

type FlowNodeData = DefinitionNodeData | BindingsNodeData;
type FlowNode = Node<FlowNodeData>;

const nodeTypes = {
  bindings: BindingsNode,
  definition: DefinitionNode,
};

const NamespaceNavigationContext = createContext<((namespaceID: string) => void) | null>(null);
const BucketNavigationContext = createContext<((bucketID: string) => void) | null>(null);

const getLayoutedElements = (nodes: FlowNode[], edges: Edge[], direction: "LR" | "TB") => {
  const g = new Dagre.graphlib.Graph().setDefaultEdgeLabel(() => ({}));
  g.setGraph({ nodesep: 56, rankdir: direction, ranksep: 96 });

  edges.forEach((edge) => g.setEdge(edge.source, edge.target));
  nodes.forEach((node) =>
    g.setNode(node.id, {
      ...node,
      height: node.measured?.height ?? 0,
      width: node.measured?.width ?? 0,
    }),
  );

  Dagre.layout(g);

  return {
    edges,
    nodes: nodes.map((node) => {
      const position = g.node(node.id);
      const x = position.x - (node.measured?.width ?? 0) / 2;
      const y = position.y - (node.measured?.height ?? 0) / 2;
      return { ...node, position: { x, y } };
    }),
  };
};

export function WorkerDefinitionFlow(props: WorkerDefinitionFlowProps) {
  return (
    <NamespaceNavigationContext.Provider value={props.onOpenNamespace}>
      <BucketNavigationContext.Provider value={props.onOpenBucket}>
        <ReactFlowProvider>
          <LayoutedWorkerDefinitionFlow {...props} />
        </ReactFlowProvider>
      </BucketNavigationContext.Provider>
    </NamespaceNavigationContext.Provider>
  );
}

function LayoutedWorkerDefinitionFlow({ deployment, namespaces, worker }: WorkerDefinitionFlowProps) {
  const { fitView } = useReactFlow();
  const nodesInitialized = useNodesInitialized();
  const containerRef = useRef<HTMLDivElement>(null);
  const initialGraph = useMemo(() => buildGraph(worker, deployment, namespaces), [deployment, namespaces, worker]);
  const graphKey = useMemo(
    () => JSON.stringify({
      bindings: deployment?.bindings ?? worker.bindings ?? [],
      hostname: worker.hostname,
      protectedRoutes: worker.auth?.protected_routes ?? [],
      workerName: worker.name,
    }),
    [deployment?.bindings, worker.auth?.protected_routes, worker.bindings, worker.hostname, worker.name],
  );
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>(initialGraph.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialGraph.edges);

  useEffect(() => {
    setNodes(initialGraph.nodes);
    setEdges(initialGraph.edges);
  }, [graphKey, initialGraph.edges, initialGraph.nodes, setEdges, setNodes]);

  useEffect(() => {
    if (!nodesInitialized) return;
    const layouted = getLayoutedElements(nodes, edges, "LR");
    setNodes([...layouted.nodes]);
    setEdges([...layouted.edges]);
    window.requestAnimationFrame(() => void fitView({ duration: 250, padding: 0.16 }));
  // We only want to relayout when measured nodes become ready or the graph shape changes.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodesInitialized, graphKey, fitView, setEdges, setNodes]);

  useEffect(() => {
    if (!nodesInitialized || !containerRef.current) return;
    const observer = new ResizeObserver(() => {
      window.requestAnimationFrame(() => void fitView({ duration: 250, maxZoom: 1, padding: 0.2 }));
    });
    observer.observe(containerRef.current);
    return () => observer.disconnect();
  }, [fitView, nodesInitialized]);

  return (
    <div ref={containerRef} className="h-[430px] rounded-xl border border-[#e2ddd2] bg-[radial-gradient(circle_at_top_left,_rgba(248,245,238,0.96),_rgba(252,250,246,0.9)_48%,_rgba(240,244,238,0.75))]">
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
        preventScrolling
        proOptions={{ hideAttribution: true }}
      >
        <Background color="#e9dfd2" gap={20} size={1} />
      </ReactFlow>
    </div>
  );
}

function buildGraph(worker: Worker, deployment: WorkerDefinitionFlowProps["deployment"], namespaces: KVNamespace[]) {
  const namespaceByID = new Map(namespaces.map((namespace) => [namespace.id, namespace]));
  const protectedRoutes = worker.auth?.protected_routes ?? [];
  const bindings = deployment?.bindings ?? worker.bindings ?? [];
  const bindingItems: BindingItem[] = bindings.map((binding) => {
    if (binding.kind === "asset") {
      return {
        binding: binding.binding,
        subtitle: `${binding.asset_count ?? 0} static asset${binding.asset_count === 1 ? "" : "s"}`,
        type: "asset" as const,
      };
    }
    if (binding.kind === "object_storage_bucket") {
      return {
        binding: binding.binding,
        bucketID: binding.bucket_id,
        subtitle: binding.bucket_name ?? binding.bucket_id ?? "bucket",
        type: "object" as const,
      };
    }
    return {
      binding: binding.binding,
      namespaceID: binding.namespace_id,
      subtitle: binding.namespace_name ?? namespaceByID.get(binding.namespace_id ?? "")?.name ?? binding.namespace_id ?? "namespace",
      type: "kv" as const,
    };
  });

  const nodes: FlowNode[] = [
    {
      data: {
        content: "Public domain for this isolate",
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
            content: protectedRoutes.slice(0, 3).join("  •  "),
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
    {
      data: {
        content: deployment?.entrypoint ?? "Awaiting deploy",
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
  ];

  const edges: Edge[] = [
    {
      animated: true,
      id: "domain-edge",
      source: "domain",
      style: { stroke: "#d4d0ca", strokeWidth: 1.5 },
      target: protectedRoutes.length ? "auth" : "worker",
      type: "smoothstep",
    },
    ...(protectedRoutes.length
      ? [{
          animated: true,
          id: "auth-edge",
          source: "auth",
          style: { stroke: "#d75a41", strokeWidth: 1.5 },
          target: "worker",
          type: "smoothstep",
        } satisfies Edge]
      : []),
    {
      animated: true,
      id: "worker-bindings-edge",
      source: "worker",
      style: { stroke: "#8fa197", strokeWidth: 1.5 },
      target: "bindings",
      type: "smoothstep",
    },
  ];

  return { edges, nodes };
}

function DefinitionNode({ data }: NodeProps<Node<DefinitionNodeData>>) {
  const Icon = data.icon === "domain" ? Globe2 : data.icon === "auth" ? ShieldCheck : Waypoints;

  return (
    <div
      className={cn(
        "nodrag nopan min-w-[220px] rounded-2xl border bg-[#fbf9f3]/96 p-4 shadow-[0_18px_45px_rgba(38,51,47,0.07)] backdrop-blur-sm",
        data.tone === "orange" ? "border-[#ebc6ba]" : data.tone === "sage" ? "border-[#d7dfd9]" : "border-[#ddd6cb]",
      )}
    >
      <Handle type="target" position={Position.Left} className="!h-2.5 !w-2.5 !border-2 !border-[#fbf9f3] !bg-[#d6d2cb]" />
      <Handle type="source" position={Position.Right} className="!h-2.5 !w-2.5 !border-2 !border-[#fbf9f3] !bg-[#8fa197]" />
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="font-mono text-[9px]   text-[#d35c45]">{data.eyebrow}</p>
          <h3 className="mt-2 text-sm font-extrabold text-[#26332f]">{data.title}</h3>
          {data.content ? <p className="mt-2 max-w-[220px] font-mono text-[10px] leading-5 text-[#737972]">{data.content}</p> : null}
        </div>
        <div className={cn("flex size-9 items-center justify-center rounded-full border", data.tone === "orange" ? "border-[#f0d4cb] bg-[#fff1ec] text-[#d75a41]" : data.tone === "sage" ? "border-[#dbe6dd] bg-[#eef4ed] text-[#50705a]" : "border-[#e4ddd1] bg-white/75 text-[#55615d]")}>
          <Icon className="size-4" />
        </div>
      </div>
    </div>
  );
}

function BindingsNode({ data }: NodeProps<Node<BindingsNodeData>>) {
  return (
    <div className="nodrag nopan pointer-events-auto w-[380px] overflow-hidden rounded-[24px] border border-[#d5d1cb] bg-[#fbfaf7]/98 shadow-[0_24px_55px_rgba(38,51,47,0.08)]">
      <Handle type="target" position={Position.Left} className="!h-2.5 !w-2.5 !border-2 !border-[#fbfaf7] !bg-[#d6d2cb]" />
      <div className="p-2.5">
        <div className="flex items-center justify-between rounded-[18px] border border-[#d7d2cb] bg-white/85 px-5 py-4 shadow-[0_8px_20px_rgba(38,51,47,0.08)]">
          <div className="flex items-center gap-3">
            <h3 className="text-[18px] font-semibold  text-[#202623]">{data.title}</h3>
            <span className="flex min-w-9 items-center justify-center rounded-xl bg-[#7d7c78] px-2 py-1 font-mono text-[13px] font-bold text-white">{data.items.length}</span>
          </div>
        </div>
      </div>

      <div className="divide-y divide-[#e4dfd8]">
        {data.items.length ? data.items.map((item) => (
          <BindingRow key={`${item.type}-${item.binding}-${item.namespaceID ?? item.bucketID ?? "asset"}`} item={item} />
        )) : (
          <div className="px-7 py-8">
            <p className="text-[18px] text-[#777975]">No bindings attached</p>
            <p className="mt-2 font-mono text-[11px]   text-[#9ca19a]">Deploy assets, KV namespaces, or object buckets to populate this section.</p>
          </div>
        )}
      </div>
    </div>
  );
}

function BindingRow({ item }: { item: BindingItem }) {
  const openNamespace = useContext(NamespaceNavigationContext);
  const openBucket = useContext(BucketNavigationContext);
  const isKV = item.type === "kv";
  const isObject = item.type === "object";
  const label = isKV ? "KV" : isObject ? "Object storage" : "Assets";

  return (
    <div className="pointer-events-auto px-7 py-5">
      <div className="mb-2 flex items-center gap-2">
        {isKV ? <KeyRound className="size-4 text-[#5d7667]" /> : isObject ? <DatabaseZap className="size-4 text-[#52748e]" /> : <FolderOpen className="size-4 text-[#7f6d4f]" />}
        <p className="text-[15px] font-semibold text-[#666864]">{label}</p>
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[16px] font-semibold  text-[#1f2522]">
        <span>{item.binding}</span>
        <span className="text-[#8a9089]">-&gt;</span>
        {isKV && item.namespaceID ? (
          <button
            type="button"
            onPointerDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
            }}
            onMouseDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
            }}
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              openNamespace?.(item.namespaceID!);
            }}
            className="nodrag nopan pointer-events-auto relative z-10 rounded-sm text-[#3b6550] underline decoration-[#9ec0ad] underline-offset-4 transition hover:text-[#264737]"
          >
            {item.subtitle}
          </button>
        ) : isObject && item.bucketID ? (
          <button
            type="button"
            onPointerDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
            }}
            onMouseDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
            }}
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              openBucket?.(item.bucketID!);
            }}
            className="nodrag nopan pointer-events-auto relative z-10 rounded-sm text-[#3b5e7d] underline decoration-[#b2c7da] underline-offset-4 transition hover:text-[#29435b]"
          >
            {item.subtitle}
          </button>
        ) : (
          <span>{item.subtitle}</span>
        )}
      </div>
    </div>
  );
}
