import { describe, expect, it } from "vitest";
import { CLAIM_PATTERN, claimOrder, isTemporary, retryDelay, retryNext } from "./rules";

describe("claimOrder (D7)", () => {
  it("prefers high 6, normal 3 and low 1 time in every 10 turns", () => {
    const counts = { high: 0, normal: 0, low: 0 };
    for (let t = 0; t < 10; t++) counts[claimOrder(t)[0]]++;
    expect(counts).toEqual({ high: 6, normal: 3, low: 1 });
  });

  it("falls back to the other priorities from high to low", () => {
    const lowTurn = CLAIM_PATTERN.indexOf("low");
    expect(claimOrder(lowTurn)).toEqual(["low", "high", "normal"]);
    expect(claimOrder(1)).toEqual(["normal", "high", "low"]);
  });
});

describe("retryDelay (D8)", () => {
  const base = 10_000;
  const max = 600_000;
  it.each([
    [1, 5_000, 10_000],
    [2, 10_000, 20_000],
    [3, 20_000, 40_000],
    [10, 300_000, 600_000], // capped at max
  ])("attempt %i waits between %i and %i ms", (attempt, lo, hi) => {
    expect(retryDelay(attempt, base, max, () => 0)).toBe(lo);
    expect(retryDelay(attempt, base, max, () => 0.999999)).toBeCloseTo(hi, -1);
  });

  it("stops after the last attempt", () => {
    expect(retryNext(4, 5, base, max, () => 0.5).retry).toBe(true);
    expect(retryNext(5, 5, base, max, () => 0.5).retry).toBe(false);
  });
});

describe("isTemporary (D18)", () => {
  it.each([
    [0, true],
    [408, true],
    [429, true],
    [500, true],
    [503, true],
    [400, false],
    [404, false],
    [301, false],
  ])("status %i temporary=%s", (status, want) => {
    expect(isTemporary(status)).toBe(want);
  });
});
