import { describe, expect, it } from "vitest";
import { Gcra } from "./limiter";

describe("Gcra (D15)", () => {
  it("allows the burst at once, then one slot per interval", () => {
    const l = new Gcra(85, 5);
    for (let i = 0; i < 5; i++) expect(l.allow("sms", 0).allowed).toBe(true);
    const denied = l.allow("sms", 0);
    expect(denied.allowed).toBe(false);
    if (!denied.allowed) expect(denied.retryAfterMs).toBeCloseTo(1000 / 85, 6);
  });

  it("keeps channels apart", () => {
    const l = new Gcra(1, 1);
    expect(l.allow("sms", 0).allowed).toBe(true);
    expect(l.allow("email", 0).allowed).toBe(true);
    expect(l.allow("sms", 0).allowed).toBe(false);
  });

  it("never allows more than rate + burst in any one second (D17)", () => {
    const l = new Gcra(85, 5);
    const allowed: number[] = [];
    for (let t = 0; t < 10_000; t += 0.5) if (l.allow("push", t).allowed) allowed.push(t);
    let busiest = 0;
    for (let i = 0, j = 0; i < allowed.length; i++) {
      while (allowed[i] - allowed[j] >= 1000) j++;
      busiest = Math.max(busiest, i - j + 1);
    }
    expect(busiest).toBeLessThanOrEqual(90);
    expect(allowed.length).toBeGreaterThanOrEqual(850);
  });

  it("reports free slots for the picture", () => {
    const l = new Gcra(10, 5);
    expect(l.available("sms", 0)).toBe(5);
    l.allow("sms", 0);
    l.allow("sms", 0);
    expect(l.available("sms", 0)).toBe(3);
    expect(l.available("sms", 1000)).toBe(5);
  });
});
