const ACTIVE_DELAY_MS = 1200;
const IDLE_DELAY_MS = 15000;
const ERROR_BASE_DELAY_MS = 5000;
const ERROR_MAX_DELAY_MS = 60000;

/** Keep active deployments responsive without polling an idle service every few seconds. */
export function nextWorkerPollDelay(
  hasPendingWork: boolean,
  consecutiveFailures: number,
): number {
  if (consecutiveFailures > 0) {
    return Math.min(
      ERROR_MAX_DELAY_MS,
      ERROR_BASE_DELAY_MS * 2 ** Math.min(consecutiveFailures - 1, 8),
    );
  }
  return hasPendingWork ? ACTIVE_DELAY_MS : IDLE_DELAY_MS;
}
