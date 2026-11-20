"use client";

// The architecture diagram: the building blocks as nodes, the paths a notification takes as
// edges, with dots moving along them as the simulation runs.

import { Background, BaseEdge, Controls, ReactFlow, ReactFlowProvider, getBezierPath, useReactFlow, type Edge, type EdgeProps, type Node } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useCallback, useEffect, useMemo, useRef } from "react";
import { useSim } from "@/lib/sim/store";
import { tokenBus } from "@/lib/sim/tokens";
import { nodeTypes } from "./nodes";

// TokenEdge is a quiet line plus a layer the token bus draws moving dots into.
function TokenEdge({ id, label, ...props }: EdgeProps) {
  const [path, labelX, labelY] = getBezierPath(props);
  const pathRef = useRef<SVGPathElement | null>(null);
  const layer = useCallback(
    (g: SVGGElement | null) => {
      const p = pathRef.current;
      if (g && p) tokenBus.register(id, p, g);
      else if (p) tokenBus.unregister(id, p);
    },
    [id],
  );
  return (
    <>
      <BaseEdge path={path} style={{ strokeWidth: 1.5 }} />
      <path ref={pathRef} d={path} fill="none" stroke="none" />
      <g ref={layer} />
      {label && (
        <foreignObject x={labelX - 60} y={labelY - 10} width={120} height={20} className="pointer-events-none overflow-visible">
          <div className="mx-auto w-fit rounded bg-canvas px-1 font-mono text-[9px] whitespace-nowrap text-muted-foreground">{label}</div>
        </foreignObject>
      )}
    </>
  );
}

const edgeTypes = { token: TokenEdge };

const COL = { client: 0, api: 240, db: 530, worker: 940, right: 1300 };
const WORKER_GAP = 175;

const FIT = { padding: 0.08 };

export function Flow({ dark }: { dark: boolean }) {
  return (
    <ReactFlowProvider>
      <Diagram dark={dark} />
    </ReactFlowProvider>
  );
}

function Diagram({ dark }: { dark: boolean }) {
  const workerIds = useSim((s) => s.snap.workers.map((w) => w.id).join(","));
  const { fitView } = useReactFlow();
  const first = useRef(true);

  // Workers came or went: refit once the new nodes are measured, without remounting the canvas.
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    const t = setTimeout(() => fitView({ ...FIT, duration: 300 }), 50);
    return () => clearTimeout(t);
  }, [workerIds, fitView]);

  const { nodes, edges } = useMemo(() => {
    const ids = workerIds ? workerIds.split(",").map(Number) : [];
    const span = Math.max(1, ids.length) * WORKER_GAP;
    const mid = span / 2 - 80;
    const nodes: Node[] = [
      { id: "client", type: "client", position: { x: COL.client, y: mid + 30 }, data: {} },
      { id: "api", type: "api", position: { x: COL.api, y: mid - 10 }, data: {} },
      { id: "db", type: "postgres", position: { x: COL.db, y: mid - 150 }, data: {} },
      { id: "redis", type: "redis", position: { x: COL.right, y: Math.min(mid - 150, 0) }, data: {} },
      { id: "provider", type: "provider", position: { x: COL.right, y: Math.max(mid + 60, 180) }, data: {} },
      ...ids.map((id, i) => ({
        id: `w${id}`,
        type: "worker",
        position: { x: COL.worker, y: i * WORKER_GAP },
        data: { workerId: id },
      })),
    ];
    const e = (id: string, source: string, target: string, sourceHandle?: string, targetHandle?: string, label?: string): Edge => ({
      id,
      source,
      target,
      sourceHandle,
      targetHandle,
      type: "token",
      label,
    });
    const edges: Edge[] = [
      e("client-api", "client", "api", "out", "in", "POST"),
      e("api-db", "api", "db", "out", "in", "INSERT"),
      ...ids.flatMap((id, i) => [
        e(`db-w${id}`, "db", `w${id}`, "claim", "claim", i === 0 ? "claim" : undefined),
        e(`w${id}-db`, `w${id}`, "db", "record", "record", i === 0 ? "result" : undefined),
        e(`w${id}-redis`, `w${id}`, "redis", "slot", "in", i === 0 ? "slot?" : undefined),
        e(`w${id}-provider`, `w${id}`, "provider", "send", "in", i === 0 ? "send" : undefined),
      ]),
    ];
    return { nodes, edges };
  }, [workerIds]);

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
      forceColorMode={dark ? "dark" : "light"}
      nodesDraggable={false}
      nodesConnectable={false}
      elementsSelectable={false}
      fitView
      fitViewOptions={FIT}
      minZoom={0.1}
      maxZoom={1.6}
      proOptions={{ hideAttribution: true }}
    >
      <Background gap={20} size={1} />
      <Controls showInteractive={false} position="bottom-left" />
    </ReactFlow>
  );
}
