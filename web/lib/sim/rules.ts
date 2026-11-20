// The service's rules, ported from the Go code so the simulation behaves like the real system.
// Each block names the decision (docs/application/02-design-decisions.md) and the Go file it mirrors.

export const CHANNELS = ["sms", "email", "push"] as const;
export type Channel = (typeof CHANNELS)[number];

export const PRIORITIES = ["high", "normal", "low"] as const;
export type Priority = (typeof PRIORITIES)[number];

// D7, internal/service/dispatcher.go claimPattern: of every 10 claims, 6 prefer high, 3 normal, 1 low.
export const CLAIM_PATTERN: readonly Priority[] = [
  "high", "normal", "high", "high", "low",
  "high", "normal", "high", "normal", "high",
];

// claimOrder is the preferred priority for this turn, then the others from high to low,
// so an empty preferred queue never wastes the claim (dispatcher.go claimOrder).
export function claimOrder(turn: number): Priority[] {
  const preferred = CLAIM_PATTERN[turn % CLAIM_PATTERN.length];
  return [preferred, ...PRIORITIES.filter((p) => p !== preferred)];
}

// D8, internal/retry/retry.go: the wait doubles from base up to max, then equal jitter picks
// a value between half of it and all of it.
export function retryDelay(attempt: number, baseMs: number, maxMs: number, rand: () => number): number {
  let d = baseMs;
  for (let i = 1; i < attempt && d < maxMs; i++) d *= 2;
  d = Math.min(d, maxMs);
  const half = d / 2;
  return half + rand() * (d - half);
}

// retryNext says what follows failed attempt number attempt: wait and try again, or stop (RET-3).
export function retryNext(
  attempt: number,
  maxAttempts: number,
  baseMs: number,
  maxMs: number,
  rand: () => number,
): { retry: true; waitMs: number } | { retry: false } {
  if (attempt >= maxAttempts) return { retry: false };
  return { retry: true, waitMs: retryDelay(attempt, baseMs, maxMs, rand) };
}

// D18, internal/provider/provider.go: which provider answers are worth retrying.
// 0 stands for "no response" (timeout, connection refused).
export function isTemporary(status: number): boolean {
  return status === 0 || status === 408 || status === 429 || status >= 500;
}

// Defaults from internal/config/config.go and docker-compose.yml.
export interface SimConfig {
  workers: number; // worker processes (docker compose --scale worker=N)
  loopsPerChannel: number; // WORKER_CONCURRENCY
  pollIntervalMs: number; // WORKER_POLL_INTERVAL
  leaseMs: number; // WORKER_LEASE_DURATION
  recoverIntervalMs: number; // WORKER_RECOVER_INTERVAL
  ratePerSecond: number; // RATE_LIMIT_PER_SECOND
  burst: number; // RATE_LIMIT_BURST
  retryMaxAttempts: number; // RETRY_MAX_ATTEMPTS
  retryBaseMs: number; // RETRY_BASE_DELAY
  retryMaxMs: number; // RETRY_MAX_DELAY
  providerLatencyMs: number; // FAKE_PROVIDER_LATENCY
  providerFailureRate: number; // FAKE_PROVIDER_FAILURE_RATE: share answered 503 (temporary)
  providerRejectRate: number; // share answered 400 (permanent); simulation only
}

export const DEFAULT_CONFIG: SimConfig = {
  workers: 1,
  loopsPerChannel: 10,
  pollIntervalMs: 500,
  leaseMs: 60_000,
  recoverIntervalMs: 10_000,
  ratePerSecond: 85,
  burst: 5,
  retryMaxAttempts: 5,
  retryBaseMs: 10_000,
  retryMaxMs: 600_000,
  providerLatencyMs: 50,
  providerFailureRate: 0,
  providerRejectRate: 0,
};

// The provider's own limit (D17): at most this many per channel in any one second.
export const PROVIDER_LIMIT_PER_SECOND = 100;
