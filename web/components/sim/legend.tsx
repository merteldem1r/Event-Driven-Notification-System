// How to read the diagram: dot color is the channel, size the priority, the result dots the outcome.

import { CHANNELS } from "@/lib/sim/rules";
import { CHANNEL_VAR, CHANNEL_LABEL } from "./shared";

export function Legend() {
  return (
    <div className="pointer-events-none absolute right-3 bottom-3 z-10 hidden flex-col gap-1.5 rounded-lg border bg-card p-2.5 text-[11px] md:flex">
      <div className="flex items-center gap-3">
        {CHANNELS.map((ch) => (
          <span key={ch} className="flex items-center gap-1">
            <span className="size-2.5 rounded-full" style={{ background: CHANNEL_VAR[ch] }} />
            {CHANNEL_LABEL[ch]}
          </span>
        ))}
        <span className="text-muted-foreground">bigger dot = higher priority</span>
      </div>
      <div className="flex items-center gap-3">
        <span className="flex items-center gap-1">
          <span className="size-2 rounded-full bg-good" /> sent
        </span>
        <span className="flex items-center gap-1">
          <span className="size-2 rounded-full bg-warn" /> retry later
        </span>
        <span className="flex items-center gap-1">
          <span className="size-2 rounded-full bg-bad" /> failed
        </span>
        <span className="flex items-center gap-1 text-muted-foreground">
          <span className="size-2.5 rounded-[3px] border-[1.5px] border-muted-foreground" /> loop waiting for a slot
        </span>
      </div>
    </div>
  );
}
