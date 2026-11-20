"use client";

// Live charts: what reaches the provider each second (the rate-limit proof) and how many rows
// wait in the queue (the burst being absorbed). They re-render once per simulated second, when
// a new point arrives, not on every snapshot.

import { memo, useMemo } from "react";
import { CartesianGrid, Line, LineChart, ReferenceLine, XAxis, YAxis } from "recharts";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ChartContainer, ChartLegend, ChartLegendContent, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart";
import type { SeriesPoint } from "@/lib/sim/engine";
import { useSim } from "@/lib/sim/store";
import { PROVIDER_LIMIT_PER_SECOND } from "@/lib/sim/rules";

const theme = (light: string, dark: string) => ({ light, dark });

const arrivalsConfig = {
  sms: { label: "SMS", theme: theme("#2a78d6", "#3987e5") },
  email: { label: "Email", theme: theme("#eb6834", "#d95926") },
  push: { label: "Push", theme: theme("#1baf7a", "#199e70") },
} satisfies ChartConfig;

const queueConfig = {
  qSms: arrivalsConfig.sms,
  qEmail: arrivalsConfig.email,
  qPush: arrivalsConfig.push,
} satisfies ChartConfig;

// Legend and tooltip follow the series order (SMS, Email, Push), not the alphabet.
const ORDER = ["sms", "email", "push", "qSms", "qEmail", "qPush"];
const bySeries = (item: { dataKey?: unknown }) => ORDER.indexOf(String(item.dataKey));

const axis = { tickLine: false, axisLine: false, tickMargin: 6, fontSize: 10 } as const;
const arrivalsDomain: [number, (max: number) => number] = [0, (max) => Math.max(120, Math.ceil(max / 20) * 20)];
const limitLabel = { value: "provider limit 100", position: "insideTopRight", fontSize: 10, fill: "var(--muted-foreground)" } as const;

type Row = SeriesPoint & { label: string };

export function Charts() {
  const series = useSim((s) => s.snap.series);
  const data = useMemo<Row[]>(() => series.map((p) => ({ ...p, label: `${p.t}s` })), [series]);
  return (
    <div data-tour="charts" className="grid gap-4 lg:grid-cols-2">
      <Card size="sm">
        <CardHeader>
          <CardTitle>Arrivals at the provider, per second</CardTitle>
          <ArrivalsDescription />
        </CardHeader>
        <CardContent>
          <ArrivalsChart data={data} />
        </CardContent>
      </Card>
      <Card size="sm">
        <CardHeader>
          <CardTitle>Rows waiting in the queue</CardTitle>
          <CardDescription>
            Pending notifications per channel, including scheduled ones and retries waiting out their backoff. The queue
            absorbs a burst and drains at the send rate.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <QueueChart data={data} />
        </CardContent>
      </Card>
    </div>
  );
}

function ArrivalsDescription() {
  const rate = useSim((s) => s.sim.config.ratePerSecond);
  const burst = useSim((s) => s.sim.config.burst);
  const peak = useSim((s) => Math.max(s.snap.channels.sms.busiestSecond, s.snap.channels.email.busiestSecond, s.snap.channels.push.busiestSecond));
  return (
    <CardDescription>
      Workers send at {rate}/s with a burst of {burst} per channel, below the provider&apos;s limit of {PROVIDER_LIMIT_PER_SECOND}.
      Busiest second so far: <span className={peak > PROVIDER_LIMIT_PER_SECOND ? "font-mono text-bad" : "font-mono text-foreground"}>{peak}</span>.
    </CardDescription>
  );
}

const ArrivalsChart = memo(function ArrivalsChart({ data }: { data: Row[] }) {
  return (
    <ChartContainer config={arrivalsConfig} className="aspect-auto h-[220px] w-full">
      <LineChart data={data} margin={{ left: -18, right: 8, top: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="label" {...axis} minTickGap={24} />
        <YAxis {...axis} domain={arrivalsDomain} />
        <ReferenceLine y={PROVIDER_LIMIT_PER_SECOND} stroke="var(--bad)" strokeWidth={1} label={limitLabel} />
        <ChartTooltip content={<ChartTooltipContent />} itemSorter={bySeries} isAnimationActive={false} />
        <ChartLegend content={<ChartLegendContent />} itemSorter={bySeries} />
        {(["sms", "email", "push"] as const).map((k) => (
          <Line key={k} dataKey={k} type="monotone" stroke={`var(--color-${k})`} strokeWidth={2} dot={false} isAnimationActive={false} />
        ))}
      </LineChart>
    </ChartContainer>
  );
});

const QueueChart = memo(function QueueChart({ data }: { data: Row[] }) {
  return (
    <ChartContainer config={queueConfig} className="aspect-auto h-[220px] w-full">
      <LineChart data={data} margin={{ left: -10, right: 8, top: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="label" {...axis} minTickGap={24} />
        <YAxis {...axis} allowDecimals={false} />
        <ChartTooltip content={<ChartTooltipContent />} itemSorter={bySeries} isAnimationActive={false} />
        <ChartLegend content={<ChartLegendContent />} itemSorter={bySeries} />
        {(["qSms", "qEmail", "qPush"] as const).map((k) => (
          <Line key={k} dataKey={k} type="monotone" stroke={`var(--color-${k})`} strokeWidth={2} dot={false} isAnimationActive={false} />
        ))}
      </LineChart>
    </ChartContainer>
  );
});
