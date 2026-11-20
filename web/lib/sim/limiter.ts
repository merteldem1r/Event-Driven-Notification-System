// The shared rate limiter (D15, internal/ratelimit/ratelimit.go). The Go service uses GCRA in
// Redis through redis_rate; this is the same algorithm, one state per channel.

import type { Channel } from "./rules";

export type AllowResult = { allowed: true } | { allowed: false; retryAfterMs: number };

// Gcra allows ratePerSecond sends per channel, with up to burst at once after an idle moment.
export class Gcra {
  private tat = new Map<Channel, number>(); // theoretical arrival time per channel

  constructor(
    private ratePerSecond: number,
    private burst: number,
  ) {}

  setLimit(ratePerSecond: number, burst: number) {
    this.ratePerSecond = ratePerSecond;
    this.burst = burst;
  }

  private get interval() {
    return 1000 / this.ratePerSecond;
  }

  // allow checks and takes a slot in one step, like the Lua script in Redis.
  allow(ch: Channel, now: number): AllowResult {
    const t = Math.max(this.tat.get(ch) ?? now, now);
    const newTat = t + this.interval;
    const diff = now - (newTat - this.interval * this.burst);
    if (diff < -1e-6) return { allowed: false, retryAfterMs: -diff };
    this.tat.set(ch, newTat);
    return { allowed: true };
  }

  // available is how many sends would be allowed right now, 0 to burst (for the picture only).
  available(ch: Channel, now: number): number {
    const ahead = Math.max(0, (this.tat.get(ch) ?? now) - now);
    const n = Math.floor((this.interval * this.burst - ahead) / this.interval + 1e-9);
    return Math.max(0, Math.min(this.burst, n));
  }
}
