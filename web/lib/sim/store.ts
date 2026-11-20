// The UI's state: one Simulation, run on animation frames, with snapshots published to React
// about ten times a second and the log four times a second.

import { create } from "zustand";
import { Simulation, type Notification, type SimEvent, type Snapshot } from "./engine";
import { CHANNELS, PRIORITIES, type Channel, type Priority, type SimConfig } from "./rules";
import { tokenBus } from "./tokens";

const SNAPSHOT_EVERY_MS = 100;
const LOG_EVERY_MS = 250;
const LOG_LIMIT = 150;
const MAX_FRAME_MS = 100; // a slow or hidden frame does not fast-forward minutes at once

export interface LogLine extends SimEvent {
  seq: number;
}

export type PanelTab = "scenarios" | "settings" | "inspect";

interface SimState {
  sim: Simulation;
  snap: Snapshot;
  running: boolean;
  speed: number;
  traffic: number; // background requests per second from the client systems
  log: LogLine[];
  problems: LogLine[]; // warnings, errors and their ends, kept longer than the busy log
  tab: PanelTab;
  selectedId: string | null;
  selected: Notification | null;
  recentIds: string[];
  lastBatchId: string | null;
  toast: string | null;

  setRunning(v: boolean): void;
  setSpeed(v: number): void;
  setTraffic(v: number): void;
  setTab(t: PanelTab): void;
  select(id: string | null, openInspector?: boolean): void;
  sendOne(channel: Channel, priority: Priority, scheduledInMs?: number): void;
  burst(perChannel: number): void;
  batch(size: number): void;
  cancelLastBatch(): void;
  cancelSelected(): void;
  setConfig(patch: Partial<SimConfig>): void;
  setRedisUp(up: boolean): void;
  killWorker(id?: number): void;
  addWorker(): void;
  reset(): void;
}

let logSeq = 0;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
// Problems are warnings and errors, plus the lines that end them, so an outage shows start and end.
const isProblem = (l: LogLine) => l.level === "warn" || l.level === "error" || l.kind === "recovered_outage" || l.kind === "redis_up";
const copyOf = (n: Notification | undefined) => (n ? { ...n, history: [...n.history] } : null);
const pick = <T>(xs: readonly T[], rand: () => number) => xs[Math.floor(rand() * xs.length)];
// A mixed load like real traffic: mostly normal, some high, some low.
const mixedPriority = (rand: () => number): Priority => {
  const r = rand();
  return r < 0.25 ? "high" : r < 0.8 ? "normal" : "low";
};

function fresh() {
  const sim = new Simulation({}, Math.floor(Math.random() * 2 ** 31));
  return { sim, snap: sim.snapshot() };
}

export const useSim = create<SimState>((set, get) => {
  const track = (ids: string[]) => set((s) => ({ recentIds: [...ids.slice(-30).reverse(), ...s.recentIds].slice(0, 30) }));
  const flash = (toast: string) => {
    clearTimeout(toastTimer);
    set({ toast });
    toastTimer = setTimeout(() => set({ toast: null }), 2500);
  };
  // After an action, show its effect at once instead of on the next snapshot.
  const refresh = () => {
    const { sim, selectedId } = get();
    set({ snap: sim.snapshot(), selected: selectedId ? copyOf(sim.get(selectedId)) : null });
  };

  return {
    ...fresh(),
    running: true,
    speed: 1,
    traffic: 12,
    log: [],
    problems: [],
    tab: "scenarios",
    selectedId: null,
    selected: null,
    recentIds: [],
    lastBatchId: null,
    toast: null,

    setRunning: (running) => set({ running }),
    setSpeed: (speed) => set({ speed }),
    setTraffic: (traffic) => set({ traffic }),
    setTab: (tab) => set({ tab }),
    select: (selectedId, openInspector = false) => {
      set({ selectedId, selected: selectedId ? copyOf(get().sim.get(selectedId)) : null, ...(openInspector ? { tab: "inspect" as const } : {}) });
    },
    sendOne: (channel, priority, scheduledInMs) => {
      const { sim } = get();
      const n = sim.submit({ channel, priority, scheduledInMs });
      sim.request("POST", "/api/v1/notifications", 202, { notification_id: n.id.slice(0, 8), channel });
      track([n.id]);
      get().select(n.id, true);
      refresh();
    },
    burst: (perChannel) => {
      const { sim } = get();
      const ids: string[] = [];
      for (const ch of CHANNELS) for (let i = 0; i < perChannel; i++) ids.push(sim.submit({ channel: ch, priority: mixedPriority(sim.rand) }).id);
      sim.request("POST", "/api/v1/notifications", 202, { requests: ids.length, note: "one line per request in the real logs" });
      track(ids);
      flash(`${ids.length.toLocaleString("en-US")} notifications accepted (202)`);
      refresh();
    },
    batch: (size) => {
      const { sim } = get();
      const items = Array.from({ length: size }, () => ({ channel: pick(CHANNELS, sim.rand), priority: pick(PRIORITIES, sim.rand) }));
      const { batchId, ids } = sim.submitBatch(items);
      sim.request("POST", "/api/v1/notifications/batch", 202, { batch_id: batchId.slice(0, 8), count: size });
      set({ lastBatchId: batchId });
      track(ids);
      flash(`batch ${batchId.slice(0, 8)}: ${size} accepted (202)`);
      refresh();
    },
    cancelLastBatch: () => {
      const { sim, lastBatchId } = get();
      if (!lastBatchId) return;
      const count = sim.cancelBatch(lastBatchId);
      sim.request("POST", `/api/v1/batches/${lastBatchId.slice(0, 8)}/cancel`, 200, { cancelled: count });
      flash(`batch cancel: ${count} cancelled, the rest already left the queue`);
      refresh();
    },
    cancelSelected: () => {
      const { sim, selectedId } = get();
      if (!selectedId) return;
      const res = sim.cancel(selectedId);
      sim.request("POST", `/api/v1/notifications/${selectedId.slice(0, 8)}/cancel`, res === "too_late" ? 409 : 200);
      flash(res === "too_late" ? "409: too late, a worker already took it" : res === "cancelled" ? "200: cancelled" : "200: already cancelled");
      refresh();
    },
    setConfig: (patch) => {
      const { sim } = get();
      sim.setConfig(patch);
      if (patch.ratePerSecond !== undefined || patch.burst !== undefined) sim.resetPeaks();
      refresh();
    },
    setRedisUp: (up) => {
      get().sim.setRedisUp(up);
      refresh();
    },
    killWorker: (id) => {
      get().sim.killWorker(id);
      refresh();
    },
    addWorker: () => {
      if (get().snap.workers.length >= 4) return;
      get().sim.startWorker();
      refresh();
    },
    reset: () => {
      tokenBus.clear();
      set({ ...fresh(), log: [], problems: [], selectedId: null, selected: null, recentIds: [], lastBatchId: null });
    },
  };
});

// startDriver runs the simulation on animation frames; it returns a stop function.
export function startDriver(): () => void {
  let frame = 0;
  let last = performance.now();
  let lastSnap = 0;
  let lastLog = 0;
  let pendingLog: LogLine[] = [];
  let pendingProblems: LogLine[] = []; // picked out before trimming, so a busy log never hides one
  let carry = 0; // background requests owed, carried between frames

  const tick = (now: number) => {
    frame = requestAnimationFrame(tick);
    const dt = Math.min(now - last, MAX_FRAME_MS);
    last = now;
    const s = useSim.getState();
    if (s.running) {
      const simDt = dt * s.speed;
      carry += (s.traffic * simDt) / 1000;
      for (; carry >= 1; carry--) s.sim.submit({ channel: pick(CHANNELS, s.sim.rand), priority: mixedPriority(s.sim.rand) });
      s.sim.advance(simDt);
    }

    const events = s.sim.drainEvents();
    if (events.length) {
      tokenBus.fromEvents(events);
      for (const e of events) {
        if (e.level === "debug") continue;
        const line = { ...e, seq: ++logSeq };
        pendingLog.push(line);
        if (isProblem(line)) pendingProblems.push(line);
      }
      if (pendingLog.length > LOG_LIMIT) pendingLog = pendingLog.slice(-LOG_LIMIT);
      if (pendingProblems.length > LOG_LIMIT) pendingProblems = pendingProblems.slice(-LOG_LIMIT);
    }

    const snapDue = now - lastSnap >= SNAPSHOT_EVERY_MS;
    const logDue = pendingLog.length > 0 && now - lastLog >= LOG_EVERY_MS;
    if (!snapDue && !logDue) return;
    const patch: Partial<SimState> = {};
    if (snapDue) {
      lastSnap = now;
      patch.snap = s.sim.snapshot();
      if (s.selectedId) patch.selected = copyOf(s.sim.get(s.selectedId));
    }
    if (logDue) {
      lastLog = now;
      patch.log = [...s.log, ...pendingLog].slice(-LOG_LIMIT);
      if (pendingProblems.length) patch.problems = [...s.problems, ...pendingProblems].slice(-LOG_LIMIT);
      pendingLog = [];
      pendingProblems = [];
    }
    useSim.setState(patch);
  };
  frame = requestAnimationFrame(tick);
  return () => cancelAnimationFrame(frame);
}
