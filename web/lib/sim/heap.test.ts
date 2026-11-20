import { describe, expect, it } from "vitest";
import { Heap } from "./heap";

describe("Heap", () => {
  it("pops in order", () => {
    const h = new Heap<number>((a, b) => a < b);
    const xs = Array.from({ length: 1000 }, (_, i) => (i * 7919) % 1000);
    xs.forEach((x) => h.push(x));
    const out: number[] = [];
    while (h.size) out.push(h.pop()!);
    expect(out).toEqual([...xs].sort((a, b) => a - b));
    expect(h.pop()).toBeUndefined();
  });
});
