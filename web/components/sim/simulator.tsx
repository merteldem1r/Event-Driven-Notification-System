"use client";

// The whole page once it runs in the browser: top bar, diagram, side panel, numbers, charts, logs.

import { Pause, Play, Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { TooltipProvider } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { startDriver, useSim } from "@/lib/sim/store";
import { Charts } from "./charts";
import { ControlPanel } from "./controls";
import { EventLog } from "./event-log";
import { Flow } from "./flow";
import { Legend } from "./legend";
import { Tour, TourButton, tourSeen, useTour } from "./tour";
import { clock, num } from "./shared";

// runScenario starts a linkable scenario, e.g. /?scenario=burst.
function runScenario(name: string | null) {
  const s = useSim.getState();
  if (name === "burst") s.burst(1000);
  if (name === "retries") s.setConfig({ providerFailureRate: 0.4 });
  if (name === "redis") s.setRedisUp(false);
  if (name === "crash") {
    s.setConfig({ workers: 2, providerLatencyMs: 1500 });
    s.batch(300);
    setTimeout(() => useSim.getState().killWorker(), 3000);
  }
}

const SPEEDS = ["0.1", "0.25", "0.5", "1", "2", "5"];
const REPO = "https://github.com/merteldem1r/Event-Driven-Notification-System";

export default function Simulator() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  useEffect(() => startDriver(), []);
  useEffect(() => runScenario(new URLSearchParams(window.location.search).get("scenario")), []);
  // The tour starts on the first visit, or from a /?tour=1 link; a scenario link skips it.
  useEffect(() => {
    const q = new URLSearchParams(window.location.search);
    if (q.get("tour") !== "1" && (q.has("scenario") || tourSeen())) return;
    const t = setTimeout(() => useTour.getState().start(), 600);
    return () => clearTimeout(t);
  }, []);
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    try {
      localStorage.setItem("theme", dark ? "dark" : "light");
    } catch {
      // storage can be blocked; the theme then lasts for this visit only
    }
  }, [dark]);

  return (
    <TooltipProvider>
      <div className="flex min-h-screen flex-col">
        <TopBar dark={dark} onTheme={() => setDark(!dark)} />
        <main className="mx-auto w-full max-w-[1600px] flex-1 space-y-4 px-4 py-4">
          <Intro />
          <div className="grid gap-4 lg:grid-cols-[1fr_340px]">
            <div data-tour="diagram" className="relative h-[420px] overflow-hidden rounded-xl border bg-canvas sm:h-[560px]">
              <Flow dark={dark} />
              <Legend />
              <Toast />
            </div>
            <div data-tour="panel" className="h-[560px] overflow-hidden rounded-xl border bg-card">
              <ControlPanel />
            </div>
          </div>
          <Totals />
          <Charts />
          <EventLog />
          <footer className="pb-6 text-center text-xs text-muted-foreground">
            A model of the Go service, running in your browser. Source and the real thing:{" "}
            <a href={REPO} className="underline underline-offset-2 hover:text-foreground">
              GitHub
            </a>
            .
          </footer>
        </main>
      </div>
      <Tour />
    </TooltipProvider>
  );
}

function TopBar({ dark, onTheme }: { dark: boolean; onTheme: () => void }) {
  const running = useSim((s) => s.running);
  const speed = useSim((s) => s.speed);
  const now = useSim((s) => s.snap.now);
  const setRunning = useSim((s) => s.setRunning);
  const setSpeed = useSim((s) => s.setSpeed);
  return (
    <header className="sticky top-0 z-20 border-b bg-background">
      <div className="mx-auto flex max-w-[1600px] flex-wrap items-center gap-3 px-4 py-2.5">
        <div className="flex w-full items-center gap-2 sm:w-auto">
          <span className="text-sm font-semibold">Notification Service</span>
          <Badge variant="secondary">simulation</Badge>
          <TourButton />
        </div>
        <div className="flex w-full items-center justify-between gap-2 sm:ml-auto sm:w-auto">
          <span className="font-mono text-xs text-muted-foreground tabular-nums">t {clock(now)}</span>
          <Button size="icon-sm" variant="outline" onClick={() => setRunning(!running)} aria-label={running ? "Pause" : "Play"}>
            {running ? <Pause /> : <Play />}
          </Button>
          <ToggleGroup type="single" size="sm" variant="outline" spacing={0} value={String(speed)} onValueChange={(v) => v && setSpeed(Number(v))}>
            {SPEEDS.map((v) => (
              <ToggleGroupItem key={v} value={v} className="px-2 font-mono text-[11px]">
                {v}×
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <Button size="icon-sm" variant="ghost" onClick={onTheme} aria-label="Toggle theme">
            {dark ? <Sun /> : <Moon />}
          </Button>
        </div>
      </div>
    </header>
  );
}

function Intro() {
  return (
    <div className="flex flex-col gap-1 md:flex-row md:items-end md:justify-between">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Requests arrive in bursts. Sending happens at a fixed rate.</h1>
        <p className="text-sm text-muted-foreground">
          The queue absorbs the difference. Every block below is a part of the service; the dots are notifications moving
          between them.
        </p>
      </div>
    </div>
  );
}

function Totals() {
  const t = useSim((s) => s.snap.totals);
  const items: [string, number, string, string][] = [
    ["created", t.created, "", "accepted with 202"],
    ["sent", t.sent, "text-good", "provider answered 202"],
    ["retries", t.retries, t.retries ? "text-warn" : "", "temporary failures, sent again later"],
    ["failed", t.failed, t.failed ? "text-bad" : "", "permanent, or out of attempts"],
    ["cancelled", t.cancelled, "", "cancelled while pending"],
    ["leases recovered", t.recovered, t.recovered ? "text-warn" : "", "taken back from a dead worker"],
  ];
  return (
    <div data-tour="totals" className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
      {items.map(([label, value, tone, hint]) => (
        <div key={label} className="rounded-xl border bg-card px-3 py-2.5">
          <div className="text-xs text-muted-foreground">{label}</div>
          <div className={cn("text-2xl font-semibold tabular-nums", tone)}>{num(value)}</div>
          <div className="truncate text-[11px] text-muted-foreground">{hint}</div>
        </div>
      ))}
    </div>
  );
}

function Toast() {
  const toast = useSim((s) => s.toast);
  if (!toast) return null;
  return (
    <div className="absolute top-3 left-1/2 z-10 -translate-x-1/2 rounded-full border bg-popover px-3 py-1 font-mono text-xs shadow-sm">
      {toast}
    </div>
  );
}
