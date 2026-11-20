"use client";

// The log, shaped like the service's JSON lines (log/slog): level, message, then key-value
// fields with the IDs. Never the recipient or the content, like the real logs.

import { memo, useLayoutEffect, useRef, useState } from "react";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { cn } from "@/lib/utils";
import { useSim, type LogLine } from "@/lib/sim/store";
import { clock } from "./shared";

const LEVEL: Record<LogLine["level"], string> = {
  debug: "text-muted-foreground",
  info: "text-ch-sms",
  warn: "text-warn",
  error: "text-bad",
};

export function EventLog() {
  const [filter, setFilter] = useState("all");
  const lines = useSim((s) => (filter === "all" ? s.log : s.problems));
  const box = useRef<HTMLDivElement>(null);
  const follow = useRef(true); // keep showing the newest lines until the reader scrolls up

  useLayoutEffect(() => {
    const el = box.current;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <Card size="sm" data-tour="logs">
      <CardHeader>
        <CardTitle>Logs</CardTitle>
        <CardDescription>What the API and workers would print. Click a line to inspect its notification.</CardDescription>
        <CardAction>
          <ToggleGroup type="single" size="sm" variant="outline" value={filter} onValueChange={(v) => v && setFilter(v)}>
            <ToggleGroupItem value="all">All</ToggleGroupItem>
            <ToggleGroupItem value="problems">Problems</ToggleGroupItem>
          </ToggleGroup>
        </CardAction>
      </CardHeader>
      <CardContent>
        <div
          ref={box}
          onScroll={(e) => {
            const el = e.currentTarget;
            follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
          }}
          className="h-[260px] overflow-auto rounded-lg bg-muted/40 p-2 font-mono text-[11px] leading-relaxed [contain:strict]"
        >
          {lines.length === 0 && (
            <div className="p-2 text-muted-foreground">{filter === "all" ? "Quiet. Send something." : "No problems so far."}</div>
          )}
          {lines.map((l) => (
            <Row key={l.seq} line={l} />
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

// Row renders one line; memo keeps old lines from re-rendering when new ones arrive.
const Row = memo(function Row({ line: l }: { line: LogLine }) {
  const select = useSim((s) => s.select);
  return (
    <div
      onClick={l.id ? () => select(l.id!, true) : undefined}
      className={cn("rounded px-1.5 whitespace-nowrap", l.id && "cursor-pointer hover:bg-muted")}
    >
      <span className="text-muted-foreground">{clock(l.t)} </span>
      <span className={cn("inline-block w-11 font-semibold uppercase", LEVEL[l.level])}>{l.level}</span>
      <span>{l.msg} </span>
      <span className="text-muted-foreground">{fields(l)}</span>
    </div>
  );
});

function fields(l: LogLine): string {
  const out: string[] = [];
  if (l.id) out.push(`notification_id=${l.id.slice(0, 8)}`);
  if (l.channel) out.push(`channel=${l.channel}`);
  if (l.priority) out.push(`priority=${l.priority}`);
  if (l.attempt) out.push(`attempt=${l.attempt}`);
  if (l.worker) out.push(`worker=worker-${l.worker}`);
  if (l.correlationId) out.push(`correlation_id=${l.correlationId.slice(0, 8)}`);
  for (const [k, v] of Object.entries(l.fields ?? {})) {
    out.push(`${k}=${typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f-]{27}$/.test(v) ? v.slice(0, 8) : String(v)}`);
  }
  return out.join(" ");
}
