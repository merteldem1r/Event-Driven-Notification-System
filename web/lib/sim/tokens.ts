// Moving dots on the diagram's edges. Drawn directly into SVG on every animation frame,
// outside React, so hundreds of messages per second stay smooth.

import type { SimEvent } from "./engine";

interface Token {
  start: number; // performance.now() when it set off
  color: string;
  r: number;
  then?: string; // edge to continue on when this one arrives
}

interface EdgeEntry {
  path: SVGPathElement;
  layer: SVGGElement;
  tokens: Token[];
  lastSpawn: number;
  d: string; // the path the lookup table was built for
  points: Float32Array; // x, y pairs sampled along the path
}

const TRAVEL_MS = 650; // real time a dot needs for one edge
const MIN_GAP_MS = 28; // at most one new dot per edge in this time; busy edges look full, not cluttered
const MAX_TOKENS = 30;
const SAMPLES = 64; // points per path; dots move between them in straight steps

const CHANNEL_COLOR: Record<string, string> = {
  sms: "var(--ch-sms)",
  email: "var(--ch-email)",
  push: "var(--ch-push)",
};
const PRIORITY_R: Record<string, number> = { high: 5, normal: 4, low: 3 };
const SVG = "http://www.w3.org/2000/svg";

class TokenBus {
  private edges = new Map<string, EdgeEntry>();
  private frame = 0;

  register(id: string, path: SVGPathElement, layer: SVGGElement) {
    this.edges.set(id, { path, layer, tokens: [], lastSpawn: 0, d: "", points: new Float32Array(0) });
  }

  unregister(id: string, path: SVGPathElement) {
    if (this.edges.get(id)?.path === path) this.edges.delete(id);
  }

  spawn(edge: string, color: string, r: number, then?: string) {
    const e = this.edges.get(edge);
    if (!e) return;
    const now = performance.now();
    if (now - e.lastSpawn < MIN_GAP_MS || e.tokens.length >= MAX_TOKENS) return;
    e.lastSpawn = now;
    e.tokens.push({ start: now, color, r, then });
    this.ensureRunning();
  }

  // fromEvents turns simulation events into dots on the matching edges.
  fromEvents(events: SimEvent[]) {
    for (const ev of events) {
      const color = ev.channel ? CHANNEL_COLOR[ev.channel] : "var(--muted-foreground)";
      const r = ev.priority ? PRIORITY_R[ev.priority] : 4;
      const w = ev.worker;
      switch (ev.kind) {
        case "accepted":
          this.spawn("client-api", color, r, "api-db");
          break;
        case "claimed":
          this.spawn(`db-w${w}`, color, r);
          break;
        case "sending":
          this.spawn(`w${w}-redis`, "var(--foreground)", 2.5);
          this.spawn(`w${w}-provider`, color, r);
          break;
        case "sent":
          this.spawn(`w${w}-db`, "var(--good)", 3.5);
          break;
        case "retry":
        case "unclaimed":
          this.spawn(`w${w}-db`, "var(--warn)", 3.5);
          break;
        case "failed":
          this.spawn(`w${w}-db`, "var(--bad)", 3.5);
          break;
      }
    }
  }

  clear() {
    for (const e of this.edges.values()) {
      e.tokens = [];
      e.layer.replaceChildren();
    }
  }

  private ensureRunning() {
    if (!this.frame && typeof window !== "undefined") this.frame = requestAnimationFrame(this.draw);
  }

  // sample rebuilds the lookup table when the edge's path changed (layout, zoom does not matter).
  private sample(e: EdgeEntry) {
    const d = e.path.getAttribute("d") ?? "";
    if (d === e.d) return;
    e.d = d;
    const len = e.path.getTotalLength();
    e.points = new Float32Array((SAMPLES + 1) * 2);
    for (let i = 0; i <= SAMPLES; i++) {
      const p = e.path.getPointAtLength((i / SAMPLES) * len);
      e.points[i * 2] = p.x;
      e.points[i * 2 + 1] = p.y;
    }
  }

  private draw = () => {
    this.frame = 0;
    const now = performance.now();
    let busy = false;
    for (const e of this.edges.values()) {
      if (!e.tokens.length && !e.layer.firstChild) continue;
      if (!e.path.isConnected) {
        e.tokens = [];
        continue;
      }
      this.sample(e);
      const arrived: Token[] = [];
      e.tokens = e.tokens.filter((t) => (now - t.start >= TRAVEL_MS ? (arrived.push(t), false) : true));

      // One circle per live token, reused from frame to frame.
      const layer = e.layer;
      while (layer.childElementCount < e.tokens.length) {
        const c = document.createElementNS(SVG, "circle");
        c.style.stroke = "var(--canvas)"; // CSS variables work in style, not in SVG attributes
        c.style.strokeWidth = "1.5";
        layer.appendChild(c);
      }
      while (layer.childElementCount > e.tokens.length) layer.lastChild!.remove();

      const pts = e.points;
      for (let i = 0; i < e.tokens.length; i++) {
        const t = e.tokens[i];
        const f = ease((now - t.start) / TRAVEL_MS) * SAMPLES;
        const k = Math.min(SAMPLES - 1, Math.floor(f));
        const u = f - k;
        const x = pts[k * 2] + (pts[k * 2 + 2] - pts[k * 2]) * u;
        const y = pts[k * 2 + 1] + (pts[k * 2 + 3] - pts[k * 2 + 1]) * u;
        const c = layer.children[i] as SVGCircleElement;
        c.setAttribute("transform", `translate(${x.toFixed(1)} ${y.toFixed(1)})`);
        if (c.dataset.color !== t.color) {
          c.dataset.color = t.color;
          c.style.fill = t.color;
        }
        const r = String(t.r);
        if (c.getAttribute("r") !== r) c.setAttribute("r", r);
      }
      for (const t of arrived) if (t.then) this.spawn(t.then, t.color, t.r);
      if (e.tokens.length) busy = true;
    }
    if (busy) this.ensureRunning();
  };
}

const ease = (x: number) => (x < 0.5 ? 2 * x * x : 1 - (-2 * x + 2) ** 2 / 2);

export const tokenBus = new TokenBus();
