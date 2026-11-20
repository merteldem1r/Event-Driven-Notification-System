"use client";

// The building blocks of the diagram, one React Flow node each. They read the latest snapshot
// from the store, so the numbers inside move with the simulation.

import { Handle, Position, type NodeProps } from "@xyflow/react";
import { Clock, Cpu, Database, Gauge, RotateCcw, Send, Server, Users, Zap, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { useSim } from "@/lib/sim/store";
import { CHANNELS, PRIORITIES, PROVIDER_LIMIT_PER_SECOND } from "@/lib/sim/rules";
import { CHANNEL_BG, CHANNEL_LABEL, CHANNEL_VAR, ChannelTag, num } from "./shared";

function Shell({
  icon: Icon,
  title,
  subtitle,
  status,
  className,
  children,
}: {
  icon: LucideIcon;
  title: string;
  subtitle: string;
  status?: "ok" | "down" | "warn";
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("rounded-xl border bg-card text-card-foreground shadow-sm", className)}>
      <div className="flex items-center gap-2 border-b px-3 py-2">
        <div className="flex size-7 items-center justify-center rounded-md bg-muted">
          <Icon className="size-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-sm leading-tight font-semibold">{title}</div>
          <div className="truncate font-mono text-[10px] text-muted-foreground">{subtitle}</div>
        </div>
        {status && (
          <span
            className={cn(
              "flex items-center gap-1 text-[10px] font-medium",
              status === "ok" && "text-good",
              status === "warn" && "text-warn",
              status === "down" && "text-bad",
            )}
          >
            <span className={cn("size-1.5 rounded-full bg-current", status === "ok" && "animate-pulse")} />
            {status === "ok" ? "healthy" : status === "warn" ? "degraded" : "down"}
          </span>
        )}
      </div>
      <div className="p-3">{children}</div>
    </div>
  );
}

const Stat = ({ label, value, className }: { label: string; value: ReactNode; className?: string }) => (
  <div className={cn("flex items-baseline justify-between gap-2 text-xs", className)}>
    <span className="text-muted-foreground">{label}</span>
    <span className="font-mono tabular-nums">{value}</span>
  </div>
);

const h = "!size-2 !border-0 !bg-transparent";

export function ClientNode() {
  const created = useSim((s) => s.snap.totals.created);
  return (
    <>
      <Shell icon={Users} title="Client systems" subtitle="HTTP callers" className="w-[190px]">
        <div className="space-y-1.5">
          <Stat label="requests made" value={num(created)} />
          <p className="pt-1 text-[11px] leading-snug text-muted-foreground">
            Send in bursts. Use the scenarios on the right.
          </p>
        </div>
      </Shell>
      <Handle type="source" position={Position.Right} id="out" className={h} />
    </>
  );
}

export function ApiNode() {
  const perSecond = useSim((s) => s.snap.apiPerSecond);
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" className={h} />
      <Shell icon={Server} title="API" subtitle="cmd/api · chi" status="ok" className="w-[220px]">
        <div className="space-y-2">
          <div className="rounded-md bg-muted px-2 py-1 font-mono text-[10px]">POST /api/v1/notifications</div>
          <ol className="space-y-0.5 text-[11px] text-muted-foreground">
            <li>1. validate the request</li>
            <li>2. insert the row (= enqueue)</li>
            <li>
              3. answer <span className="font-mono text-foreground">202 Accepted</span>
            </li>
          </ol>
          <Stat label="accepted / s" value={num(perSecond)} />
        </div>
      </Shell>
      <Handle type="source" position={Position.Right} id="out" className={h} />
    </>
  );
}

export function PostgresNode() {
  const channels = useSim((s) => s.snap.channels);
  const totals = useSim((s) => s.snap.totals);
  const maxDue = Math.max(
    100,
    ...CHANNELS.map((c) => PRIORITIES.reduce((sum, p) => sum + channels[c].due[p], 0)),
  );
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" className={h} />
      <Shell icon={Database} title="PostgreSQL" subtitle="notifications table · the queue" status="ok" className="w-[330px]">
        <div className="space-y-3">
          {CHANNELS.map((ch) => {
            const c = channels[ch];
            const due = PRIORITIES.reduce((s, p) => s + c.due[p], 0);
            return (
              <div key={ch} className="space-y-1">
                <div className="flex items-center justify-between">
                  <ChannelTag channel={ch} />
                  <span className="font-mono text-[11px] tabular-nums">
                    {num(due)} due
                    {c.oldestDueMs > 1000 && <span className="text-muted-foreground"> · oldest {(c.oldestDueMs / 1000).toFixed(0)}s</span>}
                  </span>
                </div>
                {/* Due rows by priority: segments in the channel color, strongest for high. */}
                <div className="flex h-2.5 gap-[2px] overflow-hidden rounded-[3px] bg-muted">
                  {PRIORITIES.map((p, i) =>
                    c.due[p] > 0 ? (
                      <div
                        key={p}
                        title={`${p}: ${c.due[p]}`}
                        className="h-full"
                        style={{
                          width: `${(c.due[p] / maxDue) * 100}%`,
                          background: CHANNEL_VAR[ch],
                          opacity: [1, 0.62, 0.34][i],
                        }}
                      />
                    ) : null,
                  )}
                </div>
                <div className="flex flex-wrap gap-x-2.5 gap-y-0.5 font-mono text-[10px] text-muted-foreground tabular-nums">
                  <span>H {num(c.due.high)}</span>
                  <span>N {num(c.due.normal)}</span>
                  <span>L {num(c.due.low)}</span>
                  <span className="ml-auto flex items-center gap-0.5" title="scheduled for later (D21)">
                    <Clock className="size-3" />
                    {num(c.scheduled)}
                  </span>
                  <span className="flex items-center gap-0.5" title="waiting out a retry backoff (D8)">
                    <RotateCcw className="size-3" />
                    {num(c.retryWaiting)}
                  </span>
                  <span className="flex items-center gap-0.5" title="claimed by a worker (leased)">
                    <Zap className="size-3" />
                    {num(c.processing)}
                  </span>
                </div>
              </div>
            );
          })}
          <div className="grid grid-cols-3 gap-2 border-t pt-2 text-center">
            <Outcome label="sent" value={totals.sent} className="text-good" />
            <Outcome label="failed" value={totals.failed} className={totals.failed ? "text-bad" : ""} />
            <Outcome label="cancelled" value={totals.cancelled} />
          </div>
          <p className="text-[10px] leading-snug text-muted-foreground">
            Workers claim with <span className="font-mono">FOR UPDATE SKIP LOCKED</span>, so two workers never take the
            same row.
          </p>
        </div>
      </Shell>
      <Handle type="source" position={Position.Right} id="claim" style={{ top: "38%" }} className={h} />
      <Handle type="target" position={Position.Right} id="record" style={{ top: "62%" }} className={h} />
    </>
  );
}

const Outcome = ({ label, value, className }: { label: string; value: number; className?: string }) => (
  <div>
    <div className={cn("font-mono text-sm font-semibold tabular-nums", className)}>{num(value)}</div>
    <div className="text-[10px] text-muted-foreground">{label}</div>
  </div>
);

export function WorkerNode({ data }: NodeProps) {
  const id = (data as { workerId: number }).workerId;
  const worker = useSim((s) => s.snap.workers.find((w) => w.id === id));
  const kill = useSim((s) => s.killWorker);
  if (!worker) return null;
  return (
    <>
      <Handle type="target" position={Position.Left} id="claim" style={{ top: "38%" }} className={h} />
      <Handle type="source" position={Position.Left} id="record" style={{ top: "62%" }} className={h} />
      <Shell
        icon={Cpu}
        title={`worker-${id}`}
        subtitle="cmd/worker"
        status={worker.troubled ? "warn" : "ok"}
        className="w-[260px]"
      >
        <div className="space-y-1.5">
          {CHANNELS.map((ch) => (
            <div key={ch} className="flex items-center gap-2">
              <span className="w-9 text-[10px] text-muted-foreground">{CHANNEL_LABEL[ch]}</span>
              <div className="flex flex-1 flex-wrap gap-[3px]">
                {worker.loops
                  .filter((l) => l.channel === ch)
                  .map((l, i) => (
                    <span
                      key={i}
                      title={l.state.replace("_", " ")}
                      className={cn(
                        "size-[11px] rounded-[3px]",
                        l.state === "idle" && "bg-muted",
                        l.state === "sending" && CHANNEL_BG[ch],
                        l.state === "waiting_slot" && "border-[1.5px] bg-transparent",
                      )}
                      style={l.state === "waiting_slot" ? { borderColor: CHANNEL_VAR[ch] } : undefined}
                    />
                  ))}
              </div>
            </div>
          ))}
          <div className="flex items-center justify-between border-t pt-1.5">
            <span className="font-mono text-[10px] text-muted-foreground tabular-nums">
              in flight {worker.inFlight} · sent {num(worker.sent)}
            </span>
            <button
              onClick={() => kill(id)}
              className="nodrag rounded px-1.5 py-0.5 text-[10px] font-medium text-bad hover:bg-bad/10"
              title="docker compose kill: no graceful shutdown"
            >
              kill
            </button>
          </div>
        </div>
      </Shell>
      <Handle type="source" position={Position.Right} id="slot" style={{ top: "30%" }} className={h} />
      <Handle type="source" position={Position.Right} id="send" style={{ top: "70%" }} className={h} />
    </>
  );
}

export function RedisNode() {
  const up = useSim((s) => s.snap.redisUp);
  const channels = useSim((s) => s.snap.channels);
  const cfg = useSim((s) => s.sim.config);
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" className={h} />
      <Shell icon={Gauge} title="Redis" subtitle="rate limiter · GCRA" status={up ? "ok" : "down"} className="w-[240px]">
        {up ? (
          <div className="space-y-2">
            {CHANNELS.map((ch) => (
              <div key={ch} className="flex items-center gap-2">
                <ChannelTag channel={ch} className="w-14" />
                <div className="flex gap-[3px]" title={`${channels[ch].available} of ${cfg.burst} slots free`}>
                  {Array.from({ length: cfg.burst }, (_, i) => (
                    <span
                      key={i}
                      className={cn("h-2.5 w-3 rounded-[2px]", i < channels[ch].available ? CHANNEL_BG[ch] : "bg-muted")}
                    />
                  ))}
                </div>
              </div>
            ))}
            <p className="text-[10px] leading-snug text-muted-foreground">
              {cfg.ratePerSecond}/s per channel, burst {cfg.burst}, for all workers together.
            </p>
          </div>
        ) : (
          <div className="rounded-md bg-bad/10 px-2 py-1.5 text-[11px] leading-snug text-bad">
            Unreachable. The limiter fails closed: workers put claimed rows back and send nothing.
          </div>
        )}
      </Shell>
    </>
  );
}

export function ProviderNode() {
  const channels = useSim((s) => s.snap.channels);
  const cfg = useSim((s) => s.sim.config);
  const failing = cfg.providerFailureRate + cfg.providerRejectRate;
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" className={h} />
      <Shell
        icon={Send}
        title="Provider"
        subtitle="external HTTP API"
        status={failing >= 0.5 ? "down" : failing > 0 ? "warn" : "ok"}
        className="w-[240px]"
      >
        <div className="space-y-2">
          {CHANNELS.map((ch) => {
            const c = channels[ch];
            const over = c.busiestSecond > PROVIDER_LIMIT_PER_SECOND;
            return (
              <div key={ch} className="space-y-1">
                <div className="flex items-center justify-between">
                  <ChannelTag channel={ch} />
                  <span className="font-mono text-[10px] tabular-nums">
                    {c.lastSecond}/s
                    <span className={cn("text-muted-foreground", over && "text-bad")}> · peak {c.busiestSecond}</span>
                  </span>
                </div>
                {/* Last second against the provider's limit of 100 (the mark). */}
                <div className="relative h-1.5 rounded-full bg-muted">
                  <div
                    className="h-full rounded-full"
                    style={{ width: `${Math.min(100, (c.lastSecond / 120) * 100)}%`, background: CHANNEL_VAR[ch] }}
                  />
                  <span className="absolute -top-0.5 h-2.5 w-px bg-foreground" style={{ left: `${(100 / 120) * 100}%` }} />
                </div>
              </div>
            );
          })}
          <p className="text-[10px] leading-snug text-muted-foreground">
            Limit 100/s per channel (the mark). Latency ~{cfg.providerLatencyMs}ms
            {failing > 0 && `, ${Math.round(failing * 100)}% errors`}.
          </p>
        </div>
      </Shell>
    </>
  );
}

export const nodeTypes = {
  client: ClientNode,
  api: ApiNode,
  postgres: PostgresNode,
  worker: WorkerNode,
  redis: RedisNode,
  provider: ProviderNode,
};
