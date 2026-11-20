"use client";

// The guided tour (D25): blurs and dims the page except one part, explains it in a card, and
// moves on with Next. A few steps also act on the simulation, so there is something to watch.

import { ArrowLeft, ArrowRight, Compass, Eye, X } from "lucide-react";
import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { create } from "zustand";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useSim } from "@/lib/sim/store";

interface Step {
  title: string;
  body: ReactNode;
  look?: ReactNode; // where to look while reading
  target?: () => string[]; // selectors of what to show; none shows the card in the middle
  nodes?: () => string[]; // diagram nodes to zoom to
  enter?: () => void;
  leave?: () => void;
  once?: () => void; // acts only the first time the step is shown in a tour
}

const sim = () => useSim.getState();
const node = (id: string) => `[data-tour="diagram"] .react-flow__node[data-id="${id}"]`;
const firstWorker = () => sim().snap.workers.slice(0, 1).map((w) => `w${w.id}`);
const C = ({ children }: { children: ReactNode }) => <span className="font-mono text-[12px] text-foreground">{children}</span>;
let tookRedis = false; // the tour took Redis down, so it brings it back

const STEPS: Step[] = [
  {
    title: "How this page works",
    body: (
      <>
        <p>
          This is a model of the notification service, running in your browser. Requests arrive in bursts, workers send at a
          fixed rate, and the queue in between absorbs the difference.
        </p>
        <p>The tour takes about a minute and points at each part. Everything keeps running while you read.</p>
      </>
    ),
    look: "Next and Back, or the arrow keys. Esc closes the tour; the Tour button in the top bar brings it back.",
  },
  {
    title: "1. A client calls the API",
    body: (
      <p>
        Client systems send <C>POST /api/v1/notifications</C>. The API checks the request, saves one row and answers{" "}
        <C>202 Accepted</C> at once. It never waits for the provider, so a burst of requests is answered as fast as rows can
        be saved.
      </p>
    ),
    look: (
      <>
        <C>accepted / s</C> in the API box: the requests answered in the last second.
      </>
    ),
    target: () => [node("client"), node("api")],
    nodes: () => ["client", "api"],
  },
  {
    title: "2. PostgreSQL is the queue",
    body: (
      <p>
        Saving the row is enqueuing it. A row is due now (by priority: H, N, L), scheduled for later, or waiting out a retry
        delay. Workers claim rows with <C>FOR UPDATE SKIP LOCKED</C>, so two workers never take the same row.
      </p>
    ),
    look: "We just sent a burst of 1,000 per channel. Watch the due bars fill up, then drain at the send rate.",
    target: () => [node("db")],
    nodes: () => ["db"],
    once: () => sim().burst(1000),
  },
  {
    title: "3. Workers claim and send",
    body: (
      <p>
        Each worker runs loops for every channel. A loop claims one row of its channel, asks Redis for a slot, then sends it.
        Of every 10 claims, 6 prefer high, 3 normal and 1 low, so low priority still moves during a flood of high.
      </p>
    ),
    look: "Each square is a loop: filled means sending, outlined means waiting for a slot, gray means idle.",
    target: () => firstWorker().map(node),
    nodes: firstWorker,
  },
  {
    title: "4. Redis paces all workers",
    body: (
      <p>
        One rate limiter for all workers together: 85 per second per channel with a burst of 5, so the provider never gets
        more than 100 in a second. If Redis can&apos;t be reached, workers send nothing and put the rows back: the limiter
        fails closed.
      </p>
    ),
    look: "Redis is down during this step: nothing is sent and the queue grows. It comes back when you move on.",
    target: () => [node("redis")],
    nodes: () => ["redis"],
    enter: () => {
      if (!sim().sim.isRedisUp()) return;
      tookRedis = true;
      sim().setRedisUp(false);
    },
    leave: () => {
      if (tookRedis) sim().setRedisUp(true);
      tookRedis = false;
    },
  },
  {
    title: "5. The provider delivers",
    body: (
      <p>
        The external API that delivers the message. <C>202</C> means sent. <C>503</C>, <C>429</C> and timeouts are
        temporary: the notification is tried again later, with a growing delay, up to 5 attempts. <C>400</C> is permanent: it
        fails at once.
      </p>
    ),
    look: "Each bar is the last second against the provider's limit of 100 (the mark).",
    target: () => [node("provider")],
    nodes: () => ["provider"],
  },
  {
    title: "Reading the dots",
    body: (
      <p>
        Every dot is a notification moving between blocks. Color is the channel, size is the priority. The small dots going
        back to PostgreSQL are results: green sent, amber try again later, red failed.
      </p>
    ),
    look: "The legend is in the corner. Scroll to zoom the diagram, drag to move it.",
    target: () => ['[data-tour="diagram"]'],
  },
  {
    title: "Scenarios",
    body: (
      <p>
        Throw work and trouble at the system: one notification, a burst, a batch and its cancel, provider errors, Redis down,
        or a worker that crashes in the middle of a send.
      </p>
    ),
    look: "Each button says what it does. You can try one now; the tour waits.",
    target: () => ['[data-tour="panel"]'],
    enter: () => sim().setTab("scenarios"),
  },
  {
    title: "Settings",
    body: (
      <p>
        Every slider is named after the environment variable of the Go service and starts at the same default. Change the rate
        limit or the number of workers and watch the charts answer.
      </p>
    ),
    look: "A rate plus burst above 100 is flagged: the provider would be pushed past its limit.",
    target: () => ['[data-tour="panel"]'],
    enter: () => sim().setTab("settings"),
  },
  {
    title: "One notification's story",
    body: (
      <p>
        We just sent one email. Inspect shows every step it took: accepted, claimed by a worker, sent, with its attempts and
        IDs. Click any log line or recent ID to open that notification here.
      </p>
    ),
    target: () => ['[data-tour="panel"]'],
    enter: () => sim().setTab("inspect"),
    once: () => sim().sendOne("email", "high"),
  },
  {
    title: "Totals",
    body: (
      <p>
        Counts since the start: created, sent, retries, permanent failures, cancelled, and leases taken back from a worker
        that died with a notification in hand.
      </p>
    ),
    target: () => ['[data-tour="totals"]'],
  },
  {
    title: "The charts are the proof",
    body: (
      <p>
        Arrivals at the provider: the lines stay under the limit of 100 each second, however big the burst. Rows waiting in
        the queue: a burst piles up and drains at the send rate, the queue absorbing the difference.
      </p>
    ),
    look: "Both charts show the burst from step 2 and the pause while Redis was down in step 4.",
    target: () => ['[data-tour="charts"]'],
  },
  {
    title: "Logs",
    body: (
      <p>
        Shaped like the service&apos;s JSON logs, with notification and correlation IDs, and never the message content.
        Problems shows only what went wrong and when it ended.
      </p>
    ),
    look: "Choose Problems: the Redis outage is one error when it starts and one line when it ends.",
    target: () => ['[data-tour="logs"]'],
  },
  {
    title: "Your turn",
    body: (
      <p>
        Try a burst at 5x speed, set the provider to fail half the time, or kill a worker while it sends and watch its lease
        come back. The source of the real service is linked at the bottom of the page.
      </p>
    ),
  },
];

const SEEN = "tour-seen";

// tourSeen says whether this browser has seen the tour; blocked storage counts as seen, so it
// does not start on every visit.
export function tourSeen() {
  try {
    return localStorage.getItem(SEEN) === "1";
  } catch {
    return true;
  }
}

interface TourState {
  step: number | null;
  focus: string[] | null; // diagram nodes to zoom to, read by the diagram
  start(): void;
  go(i: number): void;
  stop(): void;
}

let shown = new Set<number>();

export const useTour = create<TourState>((set, get) => ({
  step: null,
  focus: null,
  start: () => {
    shown = new Set();
    try {
      localStorage.setItem(SEEN, "1");
    } catch {
      // storage can be blocked; the tour then starts again next visit only from the button
    }
    get().go(0);
  },
  go: (i) => {
    const cur = get().step;
    if (cur !== null) STEPS[cur].leave?.();
    if (i < 0 || i >= STEPS.length) return set({ step: null, focus: null });
    const s = STEPS[i];
    s.enter?.();
    if (s.once && !shown.has(i)) s.once();
    shown.add(i);
    const focus = s.nodes?.() ?? [];
    set({ step: i, focus: focus.length ? focus : null });
  },
  stop: () => get().go(-1),
}));

export function TourButton() {
  const start = useTour((s) => s.start);
  return (
    <Button size="sm" variant="ghost" onClick={start} className="ml-auto sm:ml-0" aria-label="Start the guided tour">
      <Compass />
      Tour
    </Button>
  );
}

export function Tour() {
  const step = useTour((s) => s.step);
  return step === null ? null : createPortal(<Spotlight index={step} />, document.body);
}

interface Rect {
  top: number;
  left: number;
  width: number;
  height: number;
}

const PAD = 8;
// header is where the sticky top bar ends; a target should not hide under it (it wraps on phones).
const header = () => document.querySelector("header")?.getBoundingClientRect().bottom ?? 64;
const same = (a: Rect | null, b: Rect | null) =>
  a === b || (!!a && !!b && a.top === b.top && a.left === b.left && a.width === b.width && a.height === b.height);

// measure returns the box around all targets, cut to what the diagram shows for its nodes.
function measure(selectors: string[]): Rect | null {
  let top = Infinity,
    left = Infinity,
    right = -Infinity,
    bottom = -Infinity;
  for (const sel of selectors) {
    for (const el of document.querySelectorAll(sel)) {
      const r = el.getBoundingClientRect();
      const c = el.closest('[data-tour="diagram"]')?.getBoundingClientRect() ?? r;
      const t = Math.max(r.top, c.top),
        l = Math.max(r.left, c.left),
        b = Math.min(r.bottom, c.bottom),
        rt = Math.min(r.right, c.right);
      if (b <= t || rt <= l) continue;
      top = Math.min(top, t);
      left = Math.min(left, l);
      bottom = Math.max(bottom, b);
      right = Math.max(right, rt);
    }
  }
  if (top === Infinity) return null;
  const t = Math.max(top - PAD, header()); // never under the top bar
  return { top: Math.round(t), left: Math.round(left - PAD), width: Math.round(right - left + 2 * PAD), height: Math.round(bottom + PAD - t) };
}

// reveal scrolls the element into view below the top bar, leaving room for the card next to it.
function reveal(el: Element, card: { w: number; h: number }) {
  const r = el.getBoundingClientRect();
  const HEADER = header();
  const smooth = !matchMedia("(prefers-reduced-motion: reduce)").matches;
  const go = (want: number) => scrollTo({ top: scrollY + r.top - want, behavior: smooth ? "smooth" : "auto" });
  // Phones: the card is a sheet at the bottom, so the target goes right under the top bar.
  if (innerWidth < 640) {
    if (Math.abs(r.top - HEADER - PAD - 4) > 4) go(HEADER + PAD + 4);
    return;
  }
  const visible = r.top >= HEADER && r.bottom <= innerHeight;
  const gap = 2 * PAD + 24;
  const room =
    r.top - HEADER >= card.h + gap || innerHeight - r.bottom >= card.h + gap || r.left >= card.w + gap || innerWidth - r.right >= card.w + gap;
  if (visible && room) return;
  // Top-aligned when the card fits below it, else centered.
  const fitsBelow = r.height + card.h + HEADER + gap + PAD <= innerHeight;
  go(fitsBelow || r.height + HEADER + 24 > innerHeight ? HEADER + PAD + 4 : (innerHeight + HEADER - r.height) / 2);
}

function Spotlight({ index }: { index: number }) {
  const s = STEPS[index];
  const last = index === STEPS.length - 1;
  const go = useTour((t) => t.go);
  const stop = useTour((t) => t.stop);
  const [rect, setRect] = useState<Rect | null>(null);
  const [view, setView] = useState({ w: innerWidth, h: innerHeight, top: 64 });
  const [size, setSize] = useState({ w: 360, h: 300 });
  const sizeRef = useRef(size); // read when a step starts, without restarting the effect
  const card = useRef<HTMLDivElement>(null);
  const next = useRef<HTMLButtonElement>(null);

  // Follow the target while the page scrolls and the diagram zooms, then on scroll and resize.
  useEffect(() => {
    const targets = s.target?.() ?? [];
    const first = targets.length ? document.querySelector(targets[0]) : null;
    const box = first?.closest('[data-tour="diagram"]') ?? first; // scroll to the diagram, not a node in it
    if (box) reveal(box, { w: sizeRef.current.w, h: Math.max(sizeRef.current.h, 300) });
    const update = () => {
      const r = targets.length ? measure(targets) : null;
      setRect((prev) => (same(prev, r) ? prev : r));
      const top = Math.round(header());
      setView((v) => (v.w === innerWidth && v.h === innerHeight && v.top === top ? v : { w: innerWidth, h: innerHeight, top }));
    };
    let frame = 0;
    const until = performance.now() + 1200;
    const settle = () => {
      update();
      if (performance.now() < until) frame = requestAnimationFrame(settle);
    };
    settle();
    addEventListener("scroll", update, { passive: true });
    addEventListener("resize", update);
    return () => {
      cancelAnimationFrame(frame);
      removeEventListener("scroll", update);
      removeEventListener("resize", update);
    };
  }, [s]);

  useEffect(() => {
    const el = card.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      sizeRef.current = { w: el.offsetWidth, h: el.offsetHeight };
      setSize(sizeRef.current);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => next.current?.focus({ preventScroll: true }), [index]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement | null)?.closest?.("[role=slider], input, textarea")) return; // the key is for the control
      if (e.key === "Escape") stop();
      else if (e.key === "ArrowRight") go(index + 1);
      else if (e.key === "ArrowLeft" && index > 0) go(index - 1);
      else return;
      e.preventDefault();
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [index, go, stop]);

  const shade = "fixed z-50 bg-black/50 backdrop-blur-[2px]";
  return (
    <>
      {rect ? (
        <>
          <div className={shade} style={{ top: 0, left: 0, right: 0, height: Math.max(0, rect.top) }} />
          <div className={shade} style={{ top: rect.top + rect.height, left: 0, right: 0, bottom: 0 }} />
          <div className={shade} style={{ top: rect.top, left: 0, width: Math.max(0, rect.left), height: rect.height }} />
          <div className={shade} style={{ top: rect.top, left: rect.left + rect.width, right: 0, height: rect.height }} />
          <div className="pointer-events-none fixed z-50 rounded-xl ring-2 ring-ring" style={rect} />
        </>
      ) : (
        <div className={cn(shade, "inset-0")} />
      )}
      <div
        ref={card}
        role="dialog"
        aria-modal="true"
        aria-labelledby="tour-title"
        className="fixed z-[51] flex flex-col gap-3 rounded-xl border bg-popover p-4 text-popover-foreground shadow-lg sm:w-[360px]"
        style={place(rect, view, size)}
      >
        <div className="flex items-center justify-between">
          <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
            {index + 1} / {STEPS.length}
          </span>
          <button onClick={stop} aria-label="Close the tour" className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground">
            <X className="size-4" />
          </button>
        </div>
        <h2 id="tour-title" className="-mt-1 text-base leading-tight font-semibold">
          {s.title}
        </h2>
        <div className="space-y-2 text-sm leading-relaxed text-muted-foreground">{s.body}</div>
        {s.look && (
          <div className="flex gap-2 rounded-lg bg-muted px-2.5 py-2 text-xs leading-snug">
            <Eye className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
            <div>{s.look}</div>
          </div>
        )}
        <div className="flex gap-1" aria-hidden>
          {STEPS.map((_, i) => (
            <span key={i} className={cn("h-1 flex-1 rounded-full", i <= index ? "bg-primary" : "bg-muted")} />
          ))}
        </div>
        <div className="flex items-center justify-between gap-2">
          {last ? <span /> : (
            <Button variant="ghost" size="sm" onClick={stop} className="-ml-2 text-muted-foreground">
              Skip tour
            </Button>
          )}
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => go(index - 1)} disabled={index === 0}>
              <ArrowLeft />
              Back
            </Button>
            <Button ref={next} size="sm" onClick={() => go(index + 1)}>
              {last ? "Finish" : "Next"}
              {!last && <ArrowRight />}
            </Button>
          </div>
        </div>
      </div>
    </>
  );
}

// place puts the card next to the target: below, above, right or left; when none fits, on the
// side with the most room, overlapping only the target's edge.
function place(rect: Rect | null, view: { w: number; h: number; top: number }, size: { w: number; h: number }): CSSProperties {
  const m = 12;
  const HEADER = view.top;
  if (view.w < 640) return { left: m, right: m, bottom: m }; // phones: a sheet at the bottom
  if (!rect) return { left: (view.w - size.w) / 2, top: (view.h - size.h) / 2 };
  const x = (v: number) => Math.min(Math.max(v, m), view.w - size.w - m);
  const y = (v: number) => Math.min(Math.max(v, HEADER), view.h - size.h - m);
  const bottom = rect.top + rect.height;
  const right = rect.left + rect.width;
  if (bottom + m + size.h <= view.h - m) return { left: x(rect.left), top: bottom + m };
  if (rect.top - m - size.h >= HEADER) return { left: x(rect.left), top: rect.top - m - size.h };
  if (right + m + size.w <= view.w - m) return { left: right + m, top: y(rect.top) };
  if (rect.left - m - size.w >= m) return { left: rect.left - m - size.w, top: y(rect.top) };
  const room = [
    { side: "below", short: size.h - (view.h - bottom) },
    { side: "above", short: size.h - (rect.top - HEADER) },
    { side: "right", short: size.w - (view.w - right) },
    { side: "left", short: size.w - rect.left },
  ].sort((p, q) => p.short - q.short)[0].side;
  if (room === "below") return { left: x(rect.left), top: y(bottom + m) };
  if (room === "above") return { left: x(rect.left), top: y(rect.top - m - size.h) };
  if (room === "right") return { left: x(right + m), top: y(rect.top) };
  return { left: x(rect.left - m - size.w), top: y(rect.top) };
}
