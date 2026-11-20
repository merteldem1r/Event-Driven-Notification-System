import { cn } from "@/lib/utils";
import type { Channel, Priority } from "@/lib/sim/rules";

export const CHANNEL_LABEL: Record<Channel, string> = { sms: "SMS", email: "Email", push: "Push" };
export const PRIORITY_LABEL: Record<Priority, string> = { high: "High", normal: "Normal", low: "Low" };
export const CHANNEL_BG: Record<Channel, string> = { sms: "bg-ch-sms", email: "bg-ch-email", push: "bg-ch-push" };
export const CHANNEL_VAR: Record<Channel, string> = { sms: "var(--ch-sms)", email: "var(--ch-email)", push: "var(--ch-push)" };

// ChannelTag is the channel's color swatch with its name, so identity is never color alone.
export function ChannelTag({ channel, className }: { channel: Channel; className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", className)}>
      <span className={cn("size-2 rounded-full", CHANNEL_BG[channel])} />
      {CHANNEL_LABEL[channel]}
    </span>
  );
}

// clock formats simulated time as mm:ss.s.
export function clock(ms: number): string {
  const s = ms / 1000;
  const m = Math.floor(s / 60);
  return `${String(m).padStart(2, "0")}:${(s % 60).toFixed(1).padStart(4, "0")}`;
}

export const num = (n: number) => n.toLocaleString("en-US");
