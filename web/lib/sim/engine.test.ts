import { describe, expect, it } from "vitest";
import { Simulation } from "./engine";
import { CHANNELS } from "./rules";

describe("Simulation", () => {
  it("sends a notification in one attempt", () => {
    const sim = new Simulation();
    const n = sim.submit({ channel: "sms", priority: "high" });
    sim.advance(1_000);
    expect(n.status).toBe("sent");
    expect(n.attempts).toBe(1);
    expect(n.providerMessageId).not.toBeNull();
  });

  it("keeps every channel under 100 per second during a burst, and sends everything (IMP-9)", () => {
    const sim = new Simulation({ workers: 3 });
    for (const ch of CHANNELS) for (let i = 0; i < 1000; i++) sim.submit({ channel: ch });
    sim.advance(15_000);
    const snap = sim.snapshot();
    for (const ch of CHANNELS) {
      expect(snap.channels[ch].busiestSecond).toBeLessThanOrEqual(90);
      expect(snap.channels[ch].busiestSecond).toBeGreaterThan(80);
      expect(snap.channels[ch].sent).toBe(1000);
    }
  });

  it("sends high priority first without starving low (D7)", () => {
    const sim = new Simulation();
    for (let i = 0; i < 300; i++) sim.submit({ channel: "email", priority: "low" });
    for (let i = 0; i < 300; i++) sim.submit({ channel: "email", priority: "high" });
    sim.advance(3_000);
    const snap = sim.snapshot();
    expect(snap.channels.email.due.high).toBeLessThan(snap.channels.email.due.low);
    expect(snap.channels.email.due.low).toBeLessThan(300); // low still gets its 1 in 10
  });

  it("retries temporary failures with backoff, then gives up after 5 attempts (RET-1..3)", () => {
    const sim = new Simulation({ providerFailureRate: 1 });
    const n = sim.submit({ channel: "push" });
    sim.advance(1_000);
    expect(n.status).toBe("pending");
    expect(n.attempts).toBe(1);
    expect(n.nextAttemptAt).toBeGreaterThanOrEqual(sim.now - 1_000 + 5_000);
    sim.advance(10 * 60_000);
    expect(n.status).toBe("failed");
    expect(n.attempts).toBe(5);
  });

  it("fails a permanent rejection at once", () => {
    const sim = new Simulation({ providerRejectRate: 1 });
    const n = sim.submit({ channel: "sms" });
    sim.advance(1_000);
    expect(n.status).toBe("failed");
    expect(n.attempts).toBe(1);
  });

  it("holds a scheduled notification until its time (D21)", () => {
    const sim = new Simulation();
    const n = sim.submit({ channel: "email", scheduledInMs: 15_000 });
    sim.advance(14_000);
    expect(n.status).toBe("pending");
    expect(sim.snapshot().channels.email.scheduled).toBe(1);
    sim.advance(2_000);
    expect(n.status).toBe("sent");
  });

  it("takes back the rows of a killed worker after the lease (D19, RET-5)", () => {
    const sim = new Simulation({ workers: 2, providerLatencyMs: 2_000 });
    const n = sim.submit({ channel: "sms" });
    sim.advance(100); // claimed and sending
    expect(n.status).toBe("processing");
    sim.killWorker(n.workerId!);
    sim.advance(55_000);
    expect(n.status).toBe("processing"); // the lease has not expired yet
    sim.advance(20_000);
    expect(n.status).toBe("sent");
    expect(n.attempts).toBe(2);
    expect(sim.snapshot().totals.duplicates).toBe(1); // the provider got it twice
  });

  it("sends nothing while Redis is down and loses nothing (D15 fail closed)", () => {
    const sim = new Simulation();
    sim.setRedisUp(false);
    const n = sim.submit({ channel: "push" });
    sim.advance(5_000);
    expect(n.status).toBe("pending");
    expect(n.attempts).toBe(0); // Unclaim does not count the attempt
    sim.setRedisUp(true);
    sim.advance(1_000);
    expect(n.status).toBe("sent");
    expect(sim.drainEvents().filter((e) => e.kind === "outage")).toHaveLength(1); // logged once
  });

  it("logs an outage once even when sends were in flight as Redis went down (OBS-2)", () => {
    const sim = new Simulation({ providerLatencyMs: 200 });
    for (let i = 0; i < 300; i++) sim.submit({ channel: "sms" });
    sim.advance(2_000); // loops are busy sending
    sim.drainEvents();
    sim.setRedisUp(false);
    sim.advance(3_000); // in-flight sends finish while other loops find Redis down
    const during = sim.drainEvents();
    expect(during.filter((e) => e.kind === "outage")).toHaveLength(1);
    expect(during.filter((e) => e.kind === "recovered_outage")).toHaveLength(0);
    sim.setRedisUp(true);
    sim.advance(1_000);
    expect(sim.drainEvents().filter((e) => e.kind === "recovered_outage")).toHaveLength(1);
  });

  it("cancels only pending notifications (D14)", () => {
    const sim = new Simulation({ workers: 0 });
    const n = sim.submit({ channel: "sms" });
    expect(sim.cancel(n.id)).toBe("cancelled");
    expect(sim.cancel(n.id)).toBe("already_cancelled");
    expect(sim.cancel("nope")).toBe("not_found");

    const sim2 = new Simulation();
    const m = sim2.submit({ channel: "sms" });
    sim2.advance(1_000);
    expect(sim2.cancel(m.id)).toBe("too_late");
  });

  it("cancels what is left of a batch", () => {
    const sim = new Simulation({ workers: 0 });
    const { batchId, ids } = sim.submitBatch(Array.from({ length: 10 }, () => ({ channel: "email" as const })));
    expect(ids).toHaveLength(10);
    expect(sim.cancelBatch(batchId)).toBe(10);
  });
});

describe("Simulation bookkeeping", () => {
  // Every created notification is in exactly one place, under any mix of operations.
  it("keeps its counts equal to the rows, under random operations", () => {
    const sim = new Simulation({ workers: 2, providerFailureRate: 0.3, providerRejectRate: 0.05, leaseMs: 5_000 }, 7);
    const ids: string[] = [];
    for (let round = 0; round < 200; round++) {
      const r = sim.rand();
      if (r < 0.5) ids.push(sim.submit({ channel: CHANNELS[round % 3], scheduledInMs: r < 0.1 ? 3_000 : undefined }).id);
      else if (r < 0.6 && ids.length) sim.cancel(ids[Math.floor(sim.rand() * ids.length)]);
      else if (r < 0.63) sim.killWorker();
      else if (r < 0.66) sim.startWorker();
      else if (r < 0.7) sim.setRedisUp(sim.rand() < 0.5);
      else if (r < 0.72) sim.setConfig({ loopsPerChannel: 1 + Math.floor(sim.rand() * 15) });
      sim.advance(200 + sim.rand() * 2_000);

      const s = sim.snapshot();
      let inQueue = 0;
      let finished = 0;
      for (const ch of CHANNELS) {
        const c = s.channels[ch];
        inQueue += c.due.high + c.due.normal + c.due.low + c.scheduled + c.retryWaiting + c.processing;
        finished += c.sent + c.failed + c.cancelled;
        expect(Math.min(c.due.high, c.due.normal, c.due.low, c.scheduled, c.retryWaiting)).toBeGreaterThanOrEqual(0);
      }
      expect(inQueue + finished).toBe(s.totals.created);
    }
  });

  it("forgets old finished notifications, so memory stays flat", () => {
    const sim = new Simulation({ workers: 2, loopsPerChannel: 20, ratePerSecond: 1000, burst: 50 });
    const ids = Array.from({ length: 15_000 }, (_, i) => sim.submit({ channel: CHANNELS[i % 3] }).id);
    sim.advance(60_000);
    expect(sim.snapshot().totals.sent).toBe(15_000);
    expect(ids.filter((id) => sim.get(id))).toHaveLength(10_000); // the last 10,000 to finish are kept
  });

  it("keeps the worker count setting in step with the workers", () => {
    const sim = new Simulation();
    sim.startWorker();
    expect(sim.config.workers).toBe(2);
    sim.killWorker();
    expect(sim.config.workers).toBe(1);
    sim.setConfig({ workers: 3 });
    expect(sim.snapshot().workers).toHaveLength(3);
  });

  it("gives the same chart array until a new second is sampled", () => {
    const sim = new Simulation();
    sim.advance(1_500);
    const a = sim.snapshot().series;
    sim.advance(100);
    expect(sim.snapshot().series).toBe(a);
    sim.advance(1_000);
    expect(sim.snapshot().series).not.toBe(a);
  });

  it("drops a lowered loop count once the loops are idle", () => {
    const sim = new Simulation({ loopsPerChannel: 10 });
    sim.setConfig({ loopsPerChannel: 2 });
    sim.advance(1_000);
    expect(sim.snapshot().workers[0].loops.filter((l) => l.channel === "sms")).toHaveLength(2);
  });
});
