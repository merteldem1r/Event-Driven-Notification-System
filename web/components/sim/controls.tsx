"use client";

// The side panel: scenarios to throw at the system, its settings, and one notification's story.

import { Boxes, Clock, Flame, Info, Plus, RotateCcw, Send, Skull, XCircle } from "lucide-react";
import { useRef, useState, type ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { cn } from "@/lib/utils";
import { fmtMs, type Notification, type Status } from "@/lib/sim/engine";
import { useShallow } from "zustand/react/shallow";
import { useSim, type PanelTab } from "@/lib/sim/store";
import { CHANNELS, DEFAULT_CONFIG, PRIORITIES, type Channel, type Priority, type SimConfig } from "@/lib/sim/rules";
import { CHANNEL_LABEL, ChannelTag, PRIORITY_LABEL, clock } from "./shared";

export function ControlPanel() {
  const tab = useSim((s) => s.tab);
  const setTab = useSim((s) => s.setTab);
  const selectedId = useSim((s) => s.selectedId);
  return (
    <Tabs value={tab} onValueChange={(v) => setTab(v as PanelTab)} className="flex h-full flex-col gap-0">
      <div className="border-b p-3">
        <TabsList className="w-full">
          <TabsTrigger value="scenarios">Scenarios</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="inspect">
            Inspect
            {selectedId && <span className="size-1.5 rounded-full bg-good" />}
          </TabsTrigger>
        </TabsList>
      </div>
      <ScrollArea className="min-h-0 flex-1">
        <TabsContent value="scenarios" className="p-4">
          <Scenarios />
        </TabsContent>
        <TabsContent value="settings" className="p-4">
          <Settings />
        </TabsContent>
        <TabsContent value="inspect" className="p-4">
          <Inspector />
        </TabsContent>
      </ScrollArea>
    </Tabs>
  );
}

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="space-y-2.5">
      <div>
        <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">{title}</h3>
        {hint && <p className="mt-0.5 text-xs leading-snug text-muted-foreground">{hint}</p>}
      </div>
      {children}
    </section>
  );
}

function Scenarios() {
  // Narrow selectors: this panel re-renders only when one of these values changes.
  const s = useSim(
    useShallow((st) => ({
      sendOne: st.sendOne,
      burst: st.burst,
      batch: st.batch,
      cancelLastBatch: st.cancelLastBatch,
      lastBatchId: st.lastBatchId,
      traffic: st.traffic,
      setTraffic: st.setTraffic,
      setConfig: st.setConfig,
      failureRate: st.sim.config.providerFailureRate,
      rejectRate: st.sim.config.providerRejectRate,
      redisUp: st.snap.redisUp,
      setRedisUp: st.setRedisUp,
      killWorker: st.killWorker,
      addWorker: st.addWorker,
      workers: st.snap.workers.length,
      reset: st.reset,
    })),
  );
  const [channel, setChannel] = useState<Channel>("sms");
  const [priority, setPriority] = useState<Priority>("high");
  const workers = s.workers;

  return (
    <div className="space-y-6">
      <Section title="One notification" hint="Follow a single request through every step in the Inspect tab.">
        <ToggleGroup type="single" variant="outline" size="sm" value={channel} onValueChange={(v) => v && setChannel(v as Channel)} className="w-full">
          {CHANNELS.map((c) => (
            <ToggleGroupItem key={c} value={c} className="flex-1">
              {CHANNEL_LABEL[c]}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <ToggleGroup type="single" variant="outline" size="sm" value={priority} onValueChange={(v) => v && setPriority(v as Priority)} className="w-full">
          {PRIORITIES.map((p) => (
            <ToggleGroupItem key={p} value={p} className="flex-1">
              {PRIORITY_LABEL[p]}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <div className="grid grid-cols-2 gap-2">
          <Button size="sm" onClick={() => s.sendOne(channel, priority)}>
            <Send /> Send
          </Button>
          <Button size="sm" variant="outline" onClick={() => s.sendOne(channel, priority, 15_000)}>
            <Clock /> In 15 s
          </Button>
        </div>
      </Section>

      <Separator />

      <Section title="Traffic" hint="Requests arrive in bursts; the queue absorbs the difference.">
        <SliderRow
          label="Background traffic"
          value={s.traffic}
          format={(v) => `${v}/s`}
          min={0}
          max={300}
          step={6}
          onChange={s.setTraffic}
          note="Above 255/s (85 per channel) the queue grows, because sending is capped."
        />
        <div className="grid gap-2">
          <Button size="sm" variant="secondary" className="justify-start" onClick={() => s.burst(1000)}>
            <Flame /> Burst: 1,000 per channel
          </Button>
          <div className="grid grid-cols-2 gap-2">
            <Button size="sm" variant="secondary" className="justify-start" onClick={() => s.batch(200)}>
              <Boxes /> Batch of 200
            </Button>
            <Button size="sm" variant="outline" className="justify-start" disabled={!s.lastBatchId} onClick={s.cancelLastBatch}>
              <XCircle /> Cancel batch
            </Button>
          </div>
        </div>
      </Section>

      <Separator />

      <Section title="Break things" hint="Every failure has a planned answer. Watch the log and the diagram.">
        <SliderRow
          label="Provider answers 503"
          value={s.failureRate}
          format={(v) => `${Math.round(v * 100)}%`}
          min={0}
          max={1}
          step={0.05}
          onChange={(v) => s.setConfig({ providerFailureRate: v })}
          note="Temporary: retried with backoff, at most 5 attempts (D8, D18)."
        />
        <SliderRow
          label="Provider answers 400"
          value={s.rejectRate}
          format={(v) => `${Math.round(v * 100)}%`}
          min={0}
          max={1}
          step={0.05}
          onChange={(v) => s.setConfig({ providerRejectRate: v })}
          note="Permanent: failed at once, a retry would fail the same way."
        />
        <div className="flex items-center justify-between gap-3 rounded-lg border p-2.5">
          <div>
            <div className="text-sm font-medium">Redis down</div>
            <div className="text-xs text-muted-foreground">Fail closed: nothing sent, nothing lost (D15).</div>
          </div>
          <Switch checked={!s.redisUp} onCheckedChange={(v) => s.setRedisUp(!v)} />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <Button size="sm" variant="destructive" disabled={!workers} onClick={() => s.killWorker()}>
            <Skull /> Kill a worker
          </Button>
          <Button size="sm" variant="outline" disabled={workers >= 4} onClick={s.addWorker}>
            <Plus /> Add worker
          </Button>
        </div>
        <p className="text-xs leading-snug text-muted-foreground">
          A killed worker leaves its claimed rows in <span className="font-mono">processing</span>. After the 60 s lease,
          another worker&apos;s sweep puts them back (D19). Try it with a slow provider (Settings).
        </p>
      </Section>

      <Separator />
      <Button size="sm" variant="ghost" className="w-full" onClick={s.reset}>
        <RotateCcw /> Reset the simulation
      </Button>
    </div>
  );
}

function SliderRow({
  label,
  value,
  min,
  max,
  step,
  format,
  onChange,
  note,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  step: number;
  format: (v: number) => string;
  onChange: (v: number) => void;
  note?: string;
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between text-sm">
        <span>{label}</span>
        <span className="font-mono text-xs tabular-nums">{format(value)}</span>
      </div>
      <Slider value={[value]} min={min} max={max} step={step} onValueChange={([v]) => onChange(v)} />
      {note && <p className="text-xs leading-snug text-muted-foreground">{note}</p>}
    </div>
  );
}

interface Explained {
  what: string; // what the setting is and how it works
  why: string; // why the default is what it is
  tryIt: string; // something to change and watch
}

// Each setting is the environment variable the Go service reads, with what it means.
const SETTINGS: ({
  key: keyof SimConfig;
  env: string;
  min: number;
  max: number;
  step: number;
  format: (v: number) => string;
} & Explained)[] = [
  {
    key: "workers",
    env: "docker compose --scale worker",
    min: 0,
    max: 4,
    step: 1,
    format: String,
    what: "How many worker processes run. Each claims rows on its own; SKIP LOCKED keeps two of them off the same row. They share one rate limit through Redis.",
    why: "More workers do not send faster than the rate limit; they add room for slow sends and survive a crash.",
    tryIt: "Set 4 during a burst: the arrivals chart still stays at 85/s. Set 0: rows wait in the queue and nothing is lost.",
  },
  {
    key: "loopsPerChannel",
    env: "WORKER_CONCURRENCY",
    min: 1,
    max: 20,
    step: 1,
    format: String,
    what: "Loops per channel in each worker. A loop handles one notification at a time: claim, wait for a slot, send. Each square on a worker is one loop.",
    why: "A loop is busy for the whole send, so the rate needs about rate x latency loops: 85/s x 50 ms is about 4. Ten leaves room for a slower provider.",
    tryIt: "Set the provider latency to 1s: each loop sends once a second, so a worker manages about 10/s. Raise this to 20 and add workers: the rate climbs back toward 85.",
  },
  {
    key: "ratePerSecond",
    env: "RATE_LIMIT_PER_SECOND",
    min: 5,
    max: 150,
    step: 5,
    format: (v) => `${v}/s`,
    what: "Sends allowed per second per channel, for all workers together. Redis keeps the count (GCRA), so adding workers does not raise it.",
    why: "The provider allows 100 per second. 85 plus a burst of 5 means no second ever holds more than 90.",
    tryIt: "Set 150 and send a burst: the busiest second passes 100 and the warning below appears.",
  },
  {
    key: "burst",
    env: "RATE_LIMIT_BURST",
    min: 1,
    max: 30,
    step: 1,
    format: String,
    what: "How many sends may go at once after a quiet moment, on top of the steady rate. The Redis box shows these slots per channel.",
    why: "A small burst covers loops that are a moment late for their slot; with 1, about 10% of the slots were lost. 5 keeps rate + burst at 90, under 100.",
    tryIt: "Set 30, wait for a quiet moment, then send a burst: the first second jumps above the steady rate.",
  },
  {
    key: "providerLatencyMs",
    env: "FAKE_PROVIDER_LATENCY",
    min: 10,
    max: 3000,
    step: 10,
    format: fmtMs,
    what: "How long the fake provider takes to answer one request. The loop that sent it waits the whole time.",
    why: "Real providers answer in tens of milliseconds; 50 ms is a fair middle.",
    tryIt: "Set 1.5s: every square on the worker fills (all loops sending) and the arrival rate drops.",
  },
  {
    key: "retryMaxAttempts",
    env: "RETRY_MAX_ATTEMPTS",
    min: 1,
    max: 10,
    step: 1,
    format: String,
    what: "How many times a notification is tried in total before it fails for good. Only temporary errors are retried (503, 429, timeouts); a 400 fails at once.",
    why: "Five tries, with waits of roughly 10, 20, 40 and 80 s between them, ride out a few minutes of provider trouble without retrying forever.",
    tryIt: "In Scenarios, make the provider answer 503 half the time. With 1 attempt many fail; with 10 almost all get through, later.",
  },
  {
    key: "retryBaseMs",
    env: "RETRY_BASE_DELAY",
    min: 1000,
    max: 30_000,
    step: 1000,
    format: fmtMs,
    what: "The wait before the first retry. It doubles with each attempt, up to 10 minutes, with some randomness so retries do not all come back at the same moment.",
    why: "10 s gives a struggling provider time to recover; the waits then grow to roughly 20, 40 and 80 s.",
    tryIt: "Set 1s with 503s at 50% (Scenarios): retries come back quickly and compete with new notifications for the same slots.",
  },
  {
    key: "leaseMs",
    env: "WORKER_LEASE_DURATION",
    min: 10_000,
    max: 120_000,
    step: 5000,
    format: fmtMs,
    what: "How long a worker may hold a claimed row. If the worker dies, a sweep every 10 s takes the row back once its lease is over, and it is sent again.",
    why: "60 s is far longer than any send, so a slow send is never taken from a worker that is still alive.",
    tryIt: "With 2 workers, a slow provider and 5x speed, kill one: after the lease the other worker's sweep takes its rows back and \"leases recovered\" goes up.",
  },
];

// InfoButton explains a setting: hover opens it with a mouse, a click or tap keeps it open.
function InfoButton({ title, info, fallback }: { title: string; info: Explained; fallback: string }) {
  const [open, setOpen] = useState(false);
  const [pinned, setPinned] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const hover = (show: boolean) => (e: React.PointerEvent) => {
    if (e.pointerType !== "mouse" || pinned) return;
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setOpen(show), show ? 120 : 200); // a short delay lets the pointer reach the card
  };
  return (
    <Popover
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) setPinned(false);
      }}
    >
      <PopoverTrigger asChild>
        <button
          aria-label={`What ${title} does`}
          onPointerEnter={hover(true)}
          onPointerLeave={hover(false)}
          onClick={(e) => {
            e.preventDefault(); // decide here instead of the default toggle: a click pins an open card
            const close = open && pinned;
            setOpen(!close);
            setPinned(!close);
          }}
          className="rounded-full text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          <Info className="size-3.5" />
        </button>
      </PopoverTrigger>
      <PopoverContent
        side={innerWidth < 640 ? "bottom" : "left"} // phones have no room beside the panel
        collisionPadding={12}
        onPointerEnter={hover(true)}
        onPointerLeave={hover(false)}
        onOpenAutoFocus={(e) => e.preventDefault()} // reading it should not move the keyboard focus
        align="start"
        className="w-[min(20rem,calc(100vw-24px))] gap-2 text-xs leading-relaxed"
      >
        <div className="font-mono text-[11px] font-semibold">{title}</div>
        <p>{info.what}</p>
        <p className="text-muted-foreground">
          <span className="font-medium text-foreground">Default {fallback}.</span> {info.why}
        </p>
        <p className="rounded-md bg-muted px-2 py-1.5">
          <span className="font-medium">Try: </span>
          {info.tryIt}
        </p>
      </PopoverContent>
    </Popover>
  );
}

function Settings() {
  const config = useSim((s) => s.sim.config);
  const setConfig = useSim((s) => s.setConfig);
  const over = config.ratePerSecond + config.burst > 100;
  return (
    <div className="space-y-5">
      <p className="text-xs leading-snug text-muted-foreground">
        The defaults are the service&apos;s own (internal/config). Changing them shows why they were chosen. The{" "}
        <Info className="inline size-3 align-[-1px]" /> next to each one explains it.
      </p>
      {SETTINGS.map((st) => (
        <div key={st.key} className="space-y-2">
          <div className="flex items-center justify-between gap-2 text-sm">
            <span className="flex min-w-0 items-center gap-1.5">
              <span className="truncate font-mono text-xs">{st.env}</span>
              <InfoButton title={st.env} info={st} fallback={st.format(DEFAULT_CONFIG[st.key])} />
            </span>
            <span className="font-mono text-xs tabular-nums">{st.format(config[st.key])}</span>
          </div>
          <Slider value={[config[st.key]]} min={st.min} max={st.max} step={st.step} onValueChange={([v]) => setConfig({ [st.key]: v })} />
        </div>
      ))}
      {over && (
        <div className="rounded-md bg-bad/10 p-2.5 text-xs leading-snug text-bad">
          Rate + burst is above 100: one second can now hold more than the provider allows. This is why the service sends
          at 85/s with a burst of 5 (D17).
        </div>
      )}
    </div>
  );
}

const STATUS_TONE: Record<Status, string> = {
  pending: "bg-muted text-foreground",
  processing: "bg-ch-sms/15 text-ch-sms",
  sent: "bg-good/15 text-good",
  failed: "bg-bad/15 text-bad",
  cancelled: "bg-muted text-muted-foreground",
};

export function StatusBadge({ status }: { status: Status }) {
  return <Badge className={cn("font-mono", STATUS_TONE[status])}>{status}</Badge>;
}

function Inspector() {
  const selected = useSim((s) => s.selected);
  const recentIds = useSim((s) => s.recentIds);
  const sim = useSim((s) => s.sim);
  const select = useSim((s) => s.select);
  const cancel = useSim((s) => s.cancelSelected);
  useSim((s) => s.snap); // re-render with the simulation, so the list below stays current

  return (
    <div className="space-y-5">
      {selected ? (
        <Detail n={selected} onCancel={cancel} />
      ) : (
        <p className="text-sm text-muted-foreground">Send a notification, or pick one below, to follow it through the system.</p>
      )}
      <Section title="Recent notifications">
        <div className="space-y-1">
          {recentIds.length === 0 && <p className="text-xs text-muted-foreground">None yet.</p>}
          {recentIds.map((id) => {
            const n = sim.get(id);
            if (!n) return null;
            return (
              <button
                key={id}
                onClick={() => select(id)}
                className={cn(
                  "flex w-full items-center gap-2 rounded-md px-2 py-1 text-left hover:bg-muted",
                  selected?.id === id && "bg-muted",
                )}
              >
                <ChannelTag channel={n.channel} className="w-14" />
                <span className="flex-1 truncate font-mono text-[11px] text-muted-foreground">{id.slice(0, 13)}</span>
                <StatusBadge status={n.status} />
              </button>
            );
          })}
        </div>
      </Section>
    </div>
  );
}

const TONE: Record<string, string> = {
  info: "bg-foreground",
  good: "bg-good",
  warn: "bg-warn",
  bad: "bg-bad",
  muted: "bg-muted-foreground",
};

function Detail({ n, onCancel }: { n: Notification; onCancel: () => void }) {
  const now = useSim((s) => s.snap.now);
  const rows: [string, string][] = [
    ["id", n.id],
    ["channel", n.channel],
    ["priority", n.priority],
    ["attempts", String(n.attempts)],
    ["correlation_id", n.correlationId],
  ];
  if (n.batchId) rows.push(["batch_id", n.batchId]);
  if (n.status === "pending" && n.nextAttemptAt > now) rows.push(["next_attempt_in", fmtMs(n.nextAttemptAt - now)]);
  if (n.status === "processing" && n.lockedUntil) rows.push(["lease_left", fmtMs(Math.max(0, n.lockedUntil - now))]);
  if (n.providerMessageId) rows.push(["provider_message_id", n.providerMessageId]);
  if (n.lastError) rows.push(["last_error", n.lastError]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <ChannelTag channel={n.channel} className="text-sm" />
        <StatusBadge status={n.status} />
      </div>
      <dl className="space-y-1 rounded-lg bg-muted/50 p-2.5 font-mono text-[11px]">
        {rows.map(([k, v]) => (
          <div key={k} className="flex gap-2">
            <dt className="w-[130px] shrink-0 text-muted-foreground">{k}</dt>
            <dd className="min-w-0 break-all">{v}</dd>
          </div>
        ))}
      </dl>
      <ol className="relative space-y-3 border-l pl-4">
        {n.history.map((h, i) => (
          <li key={i} className="relative">
            <span className={cn("absolute top-1.5 -left-[20.5px] size-2 rounded-full ring-2 ring-background", TONE[h.tone])} />
            <div className="font-mono text-[10px] text-muted-foreground">t {clock(h.t)}</div>
            <div className="text-xs">{h.text}</div>
          </li>
        ))}
      </ol>
      {n.status === "pending" || n.status === "processing" ? (
        <Button size="sm" variant="outline" className="w-full" onClick={onCancel}>
          <XCircle /> Cancel (POST …/cancel)
        </Button>
      ) : null}
    </div>
  );
}
