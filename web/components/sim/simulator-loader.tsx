"use client";

// Loads the simulator in the browser only: it runs on timers and random numbers, which have no
// place in a server-side prerender.

import dynamic from "next/dynamic";

const Simulator = dynamic(() => import("./simulator"), {
  ssr: false,
  loading: () => (
    <div className="flex min-h-screen items-center justify-center text-sm text-muted-foreground">Starting the simulation…</div>
  ),
});

export function SimulatorLoader() {
  return <Simulator />;
}
