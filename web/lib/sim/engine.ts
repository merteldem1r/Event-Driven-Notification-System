// The simulation: API, PostgreSQL queue, worker loops, Redis limiter and provider, in one
// deterministic model. Time is simulated milliseconds; the UI decides how fast it runs.
//
// Built to stay cheap under load: timers and the queue are heaps (O(log n) per step), and the
// counts the page shows are kept up to date as things happen, so a snapshot never scans rows.

import { Heap } from "./heap";
import { Gcra } from "./limiter";
import {
  CHANNELS,
  DEFAULT_CONFIG,
  PRIORITIES,
  claimOrder,
  isTemporary,
  retryNext,
  type Channel,
  type Priority,
  type SimConfig,
} from "./rules";

export type Status = "pending" | "processing" | "sent" | "failed" | "cancelled";
export type Tone = "info" | "good" | "warn" | "bad" | "muted";

export interface HistoryEntry {
  t: number;
  text: string;
  tone: Tone;
}

export interface Notification {
  id: string;
  seq: number;
  channel: Channel;
  priority: Priority;
  status: Status;
  attempts: number;
  createdAt: number;
  scheduledAt: number | null;
  nextAttemptAt: number;
  lockedUntil: number | null;
  lastError: string | null;
  batchId: string | null;
  correlationId: string;
  providerMessageId: string | null;
  workerId: number | null;
  history: HistoryEntry[];
}

export type EventKind =
  | "request"
  | "accepted"
  | "claimed"
  | "sending"
  | "sent"
  | "retry"
  | "failed"
  | "unclaimed"
  | "recovered"
  | "cancelled"
  | "worker_started"
  | "worker_stopped"
  | "outage"
  | "recovered_outage"
  | "redis_down"
  | "redis_up";

// SimEvent is one thing that happened; the UI turns these into moving dots and log lines.
export interface SimEvent {
  t: number;
  kind: EventKind;
  level: "debug" | "info" | "warn" | "error";
  msg: string;
  id?: string;
  channel?: Channel;
  priority?: Priority;
  attempt?: number;
  worker?: number;
  correlationId?: string;
  fields?: Record<string, string | number | boolean>;
}

export type LoopState = "idle" | "waiting_slot" | "sending";

interface Loop {
  channel: Channel;
  state: LoopState;
  n: Notification | null;
  outcome: number; // provider status of the send in progress
  sendStart: number; // when the send in progress got its rate-limit slot
  retire: boolean; // removed once idle (concurrency lowered)
  dead: boolean; // its worker was killed
  gen: number; // bumped on every new wake-up; older timers for this loop are ignored
}

interface Worker {
  id: number;
  alive: boolean;
  loops: Loop[];
  turn: Record<Channel, number>; // position in the 6:3:1 pattern, per channel dispatcher
  troubleSince: number | null; // outage tracking, like worker.trouble in Go
  troubleTries: number;
  sent: number;
}

type Timer =
  | { at: number; seq: number; kind: "loop"; loop: Loop; worker: Worker; gen: number }
  | { at: number; seq: number; kind: "sweep"; worker: Worker }
  | { at: number; seq: number; kind: "sample" };

// Internal bookkeeping on a queued notification, kept off the public type.
interface Queued extends Notification {
  slot: "due" | "later" | null; // which queue heap holds it
  token: number; // bumped each time it is queued; older heap entries for it are ignored
  reached: boolean; // the provider received it at least once (to count duplicates)
}

interface Entry {
  n: Queued;
  token: number;
}

export interface ChannelSnapshot {
  due: Record<Priority, number>;
  scheduled: number;
  retryWaiting: number;
  processing: number;
  sent: number;
  failed: number;
  cancelled: number;
  oldestDueMs: number;
  available: number; // rate-limit slots free right now
  lastSecond: number; // provider arrivals in the last second
  busiestSecond: number; // busiest one-second window at the provider so far
}

export interface WorkerSnapshot {
  id: number;
  loops: { channel: Channel; state: LoopState; priority: Priority | null }[];
  inFlight: number;
  sent: number;
  troubled: boolean;
}

export interface SeriesPoint {
  t: number; // whole simulated second
  sms: number;
  email: number;
  push: number;
  qSms: number;
  qEmail: number;
  qPush: number;
}

export interface Snapshot {
  now: number;
  redisUp: boolean;
  channels: Record<Channel, ChannelSnapshot>;
  workers: WorkerSnapshot[];
  totals: {
    created: number;
    sent: number;
    failed: number;
    cancelled: number;
    retries: number;
    recovered: number;
    duplicates: number;
  };
  apiPerSecond: number;
  series: SeriesPoint[]; // the same array until a new second is sampled
}

export type CancelResult = "cancelled" | "already_cancelled" | "too_late" | "not_found";

export interface SubmitRequest {
  channel: Channel;
  priority?: Priority;
  scheduledInMs?: number;
}

const SERIES_LENGTH = 90; // seconds kept for the charts
const HISTORY_LIMIT = 30;
const MAX_FINISHED = 10_000; // finished notifications kept for the inspector; older ones are forgotten
const MAX_STEPS = 400_000; // per advance call, so one frame can never hang the page
const MAX_EVENTS = 20_000; // undrained events kept
const EPS = 1e-6;

// mulberry32: a small seeded random generator, so tests are repeatable.
function seeded(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// TimeWindow counts the times in the last windowMs, without shifting arrays on every add.
class TimeWindow {
  private times: number[] = [];
  private head = 0;
  constructor(private readonly windowMs: number) {}
  add(t: number) {
    this.times.push(t);
  }
  count(now: number) {
    while (this.head < this.times.length && this.times[this.head] <= now - this.windowMs) this.head++;
    if (this.head > 4096) {
      this.times = this.times.slice(this.head);
      this.head = 0;
    }
    return this.times.length - this.head;
  }
}

const emptyCounts = () => ({ high: 0, normal: 0, low: 0 }) as Record<Priority, number>;
const perChannel = <T>(make: () => T) =>
  Object.fromEntries(CHANNELS.map((c) => [c, make()])) as Record<Channel, T>;
const byDue = (a: Entry, b: Entry) =>
  a.n.nextAttemptAt < b.n.nextAttemptAt || (a.n.nextAttemptAt === b.n.nextAttemptAt && a.n.seq < b.n.seq);
const byTime = (a: Timer, b: Timer) => a.at < b.at || (a.at === b.at && a.seq < b.seq);

export class Simulation {
  now = 0;
  config: SimConfig;
  readonly rand: () => number;

  private redisUp = true;
  private limiter: Gcra;
  private all = new Map<string, Queued>();
  private finished: string[] = []; // ids in the order they finished, for forgetting old ones
  private finishedHead = 0;

  // The queue: per channel and priority, rows due now (ordered like ORDER BY next_attempt_at)
  // and rows due later (scheduled, or waiting out a retry), moved over when their time comes.
  private due = perChannel(() => ({ high: new Heap(byDue), normal: new Heap(byDue), low: new Heap(byDue) }));
  private later = perChannel(() => ({ high: new Heap(byDue), normal: new Heap(byDue), low: new Heap(byDue) }));
  private dueCount = perChannel(emptyCounts);
  private scheduledCount = perChannel(() => 0);
  private retryCount = perChannel(() => 0);
  private processing = new Set<Queued>();

  private timers = new Heap<Timer>(byTime);
  private timerSeq = 0;
  private workers: Worker[] = [];
  private nextWorkerId = 1;
  private seq = 0;
  private events: SimEvent[] = [];
  private arrivals = perChannel(() => new TimeWindow(1000)); // provider arrivals in the last second
  private busiest = perChannel(() => 0);
  private secondCounts = perChannel(() => 0);
  private apiWindow = new TimeWindow(1000);
  private series: SeriesPoint[] = [];
  private counts = { created: 0, sent: 0, failed: 0, cancelled: 0, retries: 0, recovered: 0, duplicates: 0 };
  private channelCounts = perChannel(() => ({ sent: 0, failed: 0, cancelled: 0 }));

  constructor(config: Partial<SimConfig> = {}, seed = 42) {
    this.config = { ...DEFAULT_CONFIG, ...config };
    this.rand = seeded(seed);
    this.limiter = new Gcra(this.config.ratePerSecond, this.config.burst);
    this.schedule({ at: 1000, seq: 0, kind: "sample" });
    const n = this.config.workers;
    for (let i = 0; i < n; i++) this.startWorker();
  }

  // ---- API side --------------------------------------------------------------------------

  // submit is POST /api/v1/notifications: validated, stored as pending, answered 202 at once.
  submit(req: SubmitRequest, batchId: string | null = null, correlationId?: string): Notification {
    const priority = req.priority ?? "normal"; // PROC-4: normal by default
    const scheduledAt = req.scheduledInMs ? this.now + req.scheduledInMs : null;
    const n: Queued = {
      id: this.uuid(),
      seq: ++this.seq,
      channel: req.channel,
      priority,
      status: "pending",
      attempts: 0,
      createdAt: this.now,
      scheduledAt,
      nextAttemptAt: scheduledAt ?? this.now, // D21: the queue holds it until then
      lockedUntil: null,
      lastError: null,
      batchId,
      correlationId: correlationId ?? this.uuid(),
      providerMessageId: null,
      workerId: null,
      history: [],
      slot: null,
      token: 0,
      reached: false,
    };
    this.all.set(n.id, n);
    this.enqueue(n);
    this.counts.created++;
    this.apiWindow.add(this.now);
    this.note(n, scheduledAt ? `accepted (202), scheduled in ${fmtMs(req.scheduledInMs!)}` : "accepted (202), pending", "info");
    this.emit({
      kind: "accepted",
      level: "debug", // request() logs the request line once per request
      msg: "notification created",
      id: n.id,
      channel: n.channel,
      priority,
      correlationId: n.correlationId,
      fields: { status: 202, ...(batchId ? { batch_id: batchId } : {}) },
    });
    return n;
  }

  // submitBatch is POST /api/v1/notifications/batch: one request, one correlation ID (D5).
  submitBatch(items: SubmitRequest[]): { batchId: string; ids: string[] } {
    const batchId = this.uuid();
    const corr = this.uuid();
    const ids = items.map((it) => this.submit(it, batchId, corr).id);
    return { batchId, ids };
  }

  // request logs one API request line, like the request-logging middleware.
  request(method: string, path: string, status: number, fields: Record<string, string | number> = {}) {
    this.emit({ kind: "request", level: "info", msg: "request", fields: { method, path, status, ...fields } });
  }

  // cancel is POST /api/v1/notifications/{id}/cancel (D14): only pending can be cancelled.
  cancel(id: string): CancelResult {
    const n = this.all.get(id);
    if (!n) return "not_found";
    if (n.status === "cancelled") return "already_cancelled";
    if (n.status !== "pending") return "too_late";
    this.dequeue(n);
    this.finishAs(n, "cancelled");
    this.note(n, "cancelled by the client", "muted");
    this.emit({ kind: "cancelled", level: "info", msg: "notification cancelled", id, channel: n.channel, priority: n.priority });
    return "cancelled";
  }

  // cancelBatch cancels what is still pending in a batch and says how many.
  cancelBatch(batchId: string): number {
    let count = 0;
    for (const n of this.all.values()) {
      if (n.batchId === batchId && n.status === "pending" && this.cancel(n.id) === "cancelled") count++;
    }
    return count;
  }

  get(id: string): Notification | undefined {
    return this.all.get(id);
  }

  // ---- infrastructure controls -------------------------------------------------------------

  startWorker(): number {
    const w: Worker = {
      id: this.nextWorkerId++,
      alive: true,
      loops: [],
      turn: perChannel(() => 0),
      troubleSince: null,
      troubleTries: 0,
      sent: 0,
    };
    this.workers.push(w);
    for (const ch of CHANNELS) for (let i = 0; i < this.config.loopsPerChannel; i++) this.addLoop(w, ch);
    this.schedule({ at: this.now + this.config.recoverIntervalMs, seq: 0, kind: "sweep", worker: w });
    this.syncWorkerCount();
    this.emit({
      kind: "worker_started",
      level: "info",
      msg: "worker started",
      worker: w.id,
      fields: { processors: CHANNELS.length, loops_per_processor: this.config.loopsPerChannel },
    });
    return w.id;
  }

  // killWorker is docker compose kill: no graceful shutdown, claimed rows stay processing
  // until their lease expires and another worker's sweep takes them back (D19).
  killWorker(id?: number): boolean {
    const held = (x: Worker) => x.loops.filter((l) => l.n).length;
    // Without an id, the busiest worker: that is where a crash shows the most.
    const w =
      id === undefined
        ? [...this.workers].sort((a, b) => held(b) - held(a))[0]
        : this.workers.find((x) => x.id === id);
    if (!w) return false;
    const left = held(w);
    w.alive = false;
    w.loops.forEach((l) => (l.dead = true));
    w.loops = [];
    this.workers = this.workers.filter((x) => x !== w);
    this.syncWorkerCount();
    this.emit({
      kind: "worker_stopped",
      level: "warn",
      msg: "worker killed",
      worker: w.id,
      fields: { left_processing: left, note: "rows come back after the lease expires" },
    });
    return true;
  }

  setWorkerCount(n: number) {
    while (this.workers.length < n) this.startWorker();
    while (this.workers.length > n) this.killWorker(this.workers.at(-1)!.id);
  }

  setRedisUp(up: boolean) {
    if (up === this.redisUp) return;
    this.redisUp = up;
    this.emit({
      kind: up ? "redis_up" : "redis_down",
      level: up ? "info" : "error",
      msg: up ? "redis is reachable again" : "redis unreachable: the rate limiter fails closed, nothing is sent",
    });
  }

  isRedisUp() {
    return this.redisUp;
  }

  // setConfig applies new settings; config is replaced, never mutated, so the UI sees the change.
  setConfig(patch: Partial<SimConfig>) {
    this.config = { ...this.config, ...patch };
    this.limiter.setLimit(this.config.ratePerSecond, this.config.burst);
    if (patch.loopsPerChannel !== undefined) this.resizeLoops();
    if (patch.workers !== undefined) this.setWorkerCount(patch.workers);
  }

  // ---- running -------------------------------------------------------------------------------

  // advance runs the simulation forward by ms, handling every wake-up in time order.
  advance(ms: number) {
    const target = this.now + ms;
    for (let steps = 0; steps < MAX_STEPS; steps++) {
      const t = this.timers.peek();
      if (!t || t.at > target) break;
      this.timers.pop();
      if (t.kind === "loop") {
        if (t.loop.dead || t.gen !== t.loop.gen) continue; // superseded or its worker is gone
        this.now = Math.max(this.now, t.at);
        this.step(t.worker, t.loop);
      } else if (t.kind === "sweep") {
        if (!t.worker.alive) continue;
        this.now = Math.max(this.now, t.at);
        this.sweep(t.worker);
      } else {
        this.now = Math.max(this.now, t.at);
        this.sample(t.at);
      }
    }
    this.now = Math.max(this.now, target);
  }

  // drainEvents hands the events since the last call to the UI.
  drainEvents(): SimEvent[] {
    const out = this.events;
    this.events = [];
    return out;
  }

  snapshot(): Snapshot {
    const channels = {} as Record<Channel, ChannelSnapshot>;
    for (const ch of CHANNELS) {
      for (const p of PRIORITIES) this.promote(ch, p);
      let oldest = Infinity;
      for (const p of PRIORITIES) {
        const top = this.topDue(ch, p);
        if (top) oldest = Math.min(oldest, top.nextAttemptAt);
      }
      channels[ch] = {
        due: { ...this.dueCount[ch] },
        scheduled: this.scheduledCount[ch],
        retryWaiting: this.retryCount[ch],
        processing: 0,
        ...this.channelCounts[ch],
        oldestDueMs: oldest === Infinity ? 0 : this.now - oldest,
        available: this.redisUp ? this.limiter.available(ch, this.now) : 0,
        lastSecond: this.arrivals[ch].count(this.now),
        busiestSecond: this.busiest[ch],
      };
    }
    for (const n of this.processing) channels[n.channel].processing++;
    return {
      now: this.now,
      redisUp: this.redisUp,
      channels,
      workers: this.workers.map((w) => ({
        id: w.id,
        loops: w.loops.map((l) => ({ channel: l.channel, state: l.state, priority: l.n?.priority ?? null })),
        inFlight: w.loops.filter((l) => l.n).length,
        sent: w.sent,
        troubled: w.troubleSince !== null,
      })),
      totals: { ...this.counts },
      apiPerSecond: this.apiWindow.count(this.now),
      series: this.series,
    };
  }

  // resetPeaks forgets the busiest second seen, e.g. after changing the rate limit.
  resetPeaks() {
    this.busiest = perChannel(() => 0);
  }

  // ---- the worker loop (internal/worker/worker.go + internal/service/dispatcher.go) ----------

  private step(w: Worker, l: Loop) {
    if (l.state === "idle") {
      if (l.retire) {
        l.dead = true;
        w.loops = w.loops.filter((x) => x !== l);
        return;
      }
      const n = this.claimNext(w, l.channel);
      if (!n) {
        this.wake(w, l, this.now + this.config.pollIntervalMs); // empty queue: pause, then look again
        return;
      }
      l.n = n;
      l.state = "waiting_slot";
      this.askSlot(w, l); // claim, then slot, then send (D16)
    } else if (l.state === "waiting_slot") {
      this.askSlot(w, l);
    } else {
      this.finishSend(w, l);
    }
  }

  // claimNext is ClaimNext with FOR UPDATE SKIP LOCKED: the earliest due row of the preferred
  // priority, falling back to the others; it becomes processing and is leased to the worker.
  private claimNext(w: Worker, ch: Channel): Queued | null {
    const order = claimOrder(w.turn[ch]++);
    for (const p of order) {
      this.promote(ch, p);
      const n = this.topDue(ch, p);
      if (!n) continue;
      this.due[ch][p].pop();
      this.dueCount[ch][p]--;
      n.slot = null;
      n.status = "processing";
      n.attempts++;
      n.lockedUntil = this.now + this.config.leaseMs;
      n.workerId = w.id;
      this.processing.add(n);
      this.note(n, `claimed by worker-${w.id} (attempt ${n.attempts}, ${order[0]} turn)`, "info");
      this.emit({
        kind: "claimed",
        level: "debug",
        msg: "notification claimed",
        id: n.id,
        channel: ch,
        priority: n.priority,
        attempt: n.attempts,
        worker: w.id,
        correlationId: n.correlationId,
      });
      return n;
    }
    return null;
  }

  private askSlot(w: Worker, l: Loop) {
    const n = l.n as Queued;
    if (!this.redisUp) {
      // Fail closed: no permission, so put it back without counting the attempt (Unclaim).
      this.unclaim(n);
      l.n = null;
      l.state = "idle";
      this.wake(w, l, this.now + this.config.pollIntervalMs);
      this.trouble(w, "rate limit: redis unreachable");
      return;
    }
    const res = this.limiter.allow(l.channel, this.now);
    if (!res.allowed) {
      this.wake(w, l, this.now + Math.max(res.retryAfterMs, EPS)); // Redis says when the next slot opens
      return;
    }
    this.startSend(w, l);
  }

  private startSend(w: Worker, l: Loop) {
    const n = l.n as Queued;
    const r = this.rand();
    const c = this.config;
    l.outcome = r < c.providerRejectRate ? 400 : r < c.providerRejectRate + c.providerFailureRate ? 503 : 202;
    l.state = "sending";
    l.sendStart = this.now;
    this.wake(w, l, this.now + c.providerLatencyMs * (0.7 + 0.6 * this.rand()));
    this.arrive(n);
    this.emit({
      kind: "sending",
      level: "debug",
      msg: "sending to provider",
      id: n.id,
      channel: n.channel,
      priority: n.priority,
      attempt: n.attempts,
      worker: w.id,
      correlationId: n.correlationId,
    });
  }

  private finishSend(w: Worker, l: Loop) {
    const n = l.n as Queued;
    const status = l.outcome;
    l.n = null;
    l.state = "idle";
    this.wake(w, l, this.now); // found work: look again at once, no pause
    if (!this.owns(n, w)) return; // fencing: the lease expired and someone else took it

    const base = {
      id: n.id,
      channel: n.channel,
      priority: n.priority,
      attempt: n.attempts,
      worker: w.id,
      correlationId: n.correlationId,
    };
    if (status === 202) {
      n.providerMessageId = this.uuid();
      this.finishAs(n, "sent");
      w.sent++;
      this.recoveredFrom(w, l.sendStart);
      this.note(n, `sent (202), provider message ${n.providerMessageId.slice(0, 8)}`, "good");
      this.emit({ ...base, kind: "sent", level: "info", msg: "notification sent", fields: { provider_message_id: n.providerMessageId } });
      return;
    }

    const err = status === 503 ? "provider answered 503 Service Unavailable" : "provider answered 400 Bad Request";
    n.lastError = err;
    if (isTemporary(status)) {
      const { retryMaxAttempts, retryBaseMs, retryMaxMs } = this.config;
      const next = retryNext(n.attempts, retryMaxAttempts, retryBaseMs, retryMaxMs, this.rand);
      if (next.retry) {
        this.processing.delete(n);
        n.status = "pending";
        n.lockedUntil = null;
        n.nextAttemptAt = this.now + next.waitMs;
        this.enqueue(n);
        this.counts.retries++;
        this.note(n, `503, temporary: retry in ${fmtMs(next.waitMs)}`, "warn");
        this.emit({
          ...base,
          kind: "retry",
          level: "warn",
          msg: "notification failed, will retry",
          fields: { error: err, temporary: true, retry_in: fmtMs(next.waitMs) },
        });
        return;
      }
    }
    const reason = isTemporary(status) ? "no attempts left" : "rejected by the provider";
    this.finishAs(n, "failed");
    this.note(n, `failed: ${reason}`, "bad");
    this.emit({
      ...base,
      kind: "failed",
      level: "warn",
      msg: "notification failed",
      fields: { error: err, temporary: isTemporary(status), reason },
    });
  }

  // sweep is RecoverExpired, every WORKER_RECOVER_INTERVAL in every worker (D19).
  private sweep(w: Worker) {
    this.schedule({ at: this.now + this.config.recoverIntervalMs, seq: 0, kind: "sweep", worker: w });
    let count = 0;
    for (const n of this.processing) {
      if (n.lockedUntil === null || n.lockedUntil >= this.now) continue;
      this.processing.delete(n); // deleting the current item is safe while iterating a Set
      n.status = "pending";
      n.lockedUntil = null;
      n.workerId = null;
      n.lastError = "lease expired: the worker stopped without a result";
      this.enqueue(n);
      this.counts.recovered++;
      count++;
      this.note(n, `lease expired, taken back by worker-${w.id}'s sweep`, "warn");
    }
    if (count > 0) {
      this.emit({
        kind: "recovered",
        level: "warn",
        msg: "took back notifications whose worker stopped without a result",
        worker: w.id,
        fields: { count, note: "they are sent again; the lease had expired" },
      });
    }
  }

  // ---- the queue -------------------------------------------------------------------------------

  // enqueue puts a pending notification in the right heap and counts it.
  private enqueue(n: Queued) {
    const entry = { n, token: ++n.token };
    if (n.nextAttemptAt <= this.now) {
      n.slot = "due";
      this.due[n.channel][n.priority].push(entry);
      this.dueCount[n.channel][n.priority]++;
    } else {
      n.slot = "later";
      this.later[n.channel][n.priority].push(entry);
      if (n.attempts === 0) this.scheduledCount[n.channel]++;
      else this.retryCount[n.channel]++;
    }
  }

  // dequeue takes a pending notification out of the counts; its heap entry goes stale.
  private dequeue(n: Queued) {
    if (n.slot === "due") this.dueCount[n.channel][n.priority]--;
    else if (n.slot === "later") this.uncountLater(n);
    n.slot = null;
    n.token++;
  }

  private uncountLater(n: Queued) {
    if (n.attempts === 0) this.scheduledCount[n.channel]--;
    else this.retryCount[n.channel]--;
  }

  // promote moves rows whose time has come from "later" to "due".
  private promote(ch: Channel, p: Priority) {
    const later = this.later[ch][p];
    for (let e = later.peek(); e; e = later.peek()) {
      if (e.token !== e.n.token) {
        later.pop(); // stale: cancelled or queued again since
        continue;
      }
      if (e.n.nextAttemptAt > this.now) break;
      later.pop();
      this.uncountLater(e.n);
      this.enqueue(e.n);
    }
  }

  // topDue is the earliest due row, dropping stale heap entries on the way.
  private topDue(ch: Channel, p: Priority): Queued | null {
    const due = this.due[ch][p];
    for (let e = due.peek(); e; e = due.peek()) {
      if (e.token === e.n.token && e.n.slot === "due") return e.n;
      due.pop();
    }
    return null;
  }

  // ---- helpers -------------------------------------------------------------------------------

  private unclaim(n: Queued) {
    this.processing.delete(n);
    n.status = "pending";
    n.attempts--;
    n.lockedUntil = null;
    n.workerId = null;
    this.enqueue(n);
    this.note(n, "no rate-limit slot (redis down): back in the queue, attempt not counted", "warn");
    this.emit({ kind: "unclaimed", level: "debug", msg: "put back: no rate-limit slot", id: n.id, channel: n.channel, priority: n.priority });
  }

  // trouble logs the first failure of an outage only, like the Go worker (OBS-2).
  private trouble(w: Worker, error: string) {
    if (w.troubleSince === null) {
      w.troubleSince = this.now;
      w.troubleTries = 1;
      this.emit({ kind: "outage", level: "error", msg: "processing failed", worker: w.id, fields: { error, note: "retries are logged at debug level" } });
    } else w.troubleTries++;
  }

  // recoveredFrom ends an outage on a delivery. A send that got its slot before the outage began
  // proves nothing: in Go, the failing Redis call takes its timeout, so those sends finish first.
  private recoveredFrom(w: Worker, sendStart: number) {
    if (w.troubleSince === null || sendStart <= w.troubleSince) return;
    this.emit({
      kind: "recovered_outage",
      level: "info",
      msg: "working again",
      worker: w.id,
      fields: { down_for: fmtMs(this.now - w.troubleSince), failed_tries: w.troubleTries },
    });
    w.troubleSince = null;
    w.troubleTries = 0;
  }

  private owns(n: Queued, w: Worker) {
    return n.status === "processing" && n.workerId === w.id;
  }

  private finishAs(n: Queued, status: "sent" | "failed" | "cancelled") {
    this.processing.delete(n);
    n.status = status;
    n.lockedUntil = null;
    this.counts[status]++;
    this.channelCounts[n.channel][status]++;
    this.finished.push(n.id);
    this.forgetOld();
  }

  // forgetOld drops the oldest finished notifications, so memory stays flat in a long session.
  private forgetOld() {
    while (this.finished.length - this.finishedHead > MAX_FINISHED) {
      this.all.delete(this.finished[this.finishedHead++]);
    }
    if (this.finishedHead > 50_000) {
      this.finished = this.finished.slice(this.finishedHead);
      this.finishedHead = 0;
    }
  }

  // arrive counts a request reaching the provider, the way the fake provider measures it (D17).
  private arrive(n: Queued) {
    const ch = n.channel;
    this.arrivals[ch].add(this.now);
    this.busiest[ch] = Math.max(this.busiest[ch], this.arrivals[ch].count(this.now));
    this.secondCounts[ch]++;
    if (n.reached) this.counts.duplicates++;
    n.reached = true;
  }

  private sample(at: number) {
    const depth = (ch: Channel) =>
      PRIORITIES.reduce((s, p) => s + this.dueCount[ch][p], 0) + this.scheduledCount[ch] + this.retryCount[ch];
    // A new array each second, so the charts re-render only when there is a new point.
    this.series = [
      ...this.series.slice(-(SERIES_LENGTH - 1)),
      {
        t: Math.round(at / 1000),
        sms: this.secondCounts.sms,
        email: this.secondCounts.email,
        push: this.secondCounts.push,
        qSms: depth("sms"),
        qEmail: depth("email"),
        qPush: depth("push"),
      },
    ];
    this.secondCounts = perChannel(() => 0);
    this.schedule({ at: at + 1000, seq: 0, kind: "sample" });
  }

  private schedule(t: Timer) {
    t.seq = ++this.timerSeq;
    this.timers.push(t);
  }

  // wake sets when a loop runs next; any earlier timer for it is ignored from now on.
  private wake(w: Worker, l: Loop, at: number) {
    l.gen++;
    this.schedule({ at, seq: 0, kind: "loop", loop: l, worker: w, gen: l.gen });
  }

  private addLoop(w: Worker, ch: Channel) {
    const l: Loop = { channel: ch, state: "idle", n: null, outcome: 0, sendStart: 0, retire: false, dead: false, gen: 0 };
    w.loops.push(l);
    // Loops start at a random point of the poll interval, like goroutines that are not in lockstep.
    this.wake(w, l, this.now + this.rand() * this.config.pollIntervalMs);
  }

  private resizeLoops() {
    for (const w of this.workers) {
      for (const ch of CHANNELS) {
        const active = w.loops.filter((l) => l.channel === ch && !l.retire);
        const want = this.config.loopsPerChannel;
        for (let i = active.length; i < want; i++) this.addLoop(w, ch);
        active.slice(want).forEach((l) => (l.retire = true));
      }
    }
  }

  private syncWorkerCount() {
    if (this.config.workers !== this.workers.length) this.config = { ...this.config, workers: this.workers.length };
  }

  private note(n: Notification, text: string, tone: Tone) {
    n.history.push({ t: this.now, text, tone });
    if (n.history.length > HISTORY_LIMIT) n.history.shift();
  }

  private emit(e: Omit<SimEvent, "t">) {
    this.events.push({ t: this.now, ...e });
    if (this.events.length > MAX_EVENTS) this.events.splice(0, MAX_EVENTS / 2); // nobody is draining
  }

  // uuid makes a version 4 UUID from the seeded generator, 32 bits at a time.
  private uuid(): string {
    let h = "";
    for (let i = 0; i < 4; i++) h += Math.floor(this.rand() * 0x100000000).toString(16).padStart(8, "0");
    const variant = "89ab"[parseInt(h[16], 16) & 3];
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-${variant}${h.slice(17, 20)}-${h.slice(20, 32)}`;
  }
}

// fmtMs formats a duration the way Go prints one, roughly: 850ms, 12.4s, 3m5s.
export function fmtMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return s ? `${m}m${s}s` : `${m}m`;
}
