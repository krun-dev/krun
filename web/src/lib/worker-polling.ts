import { APIError } from "./api.ts";

const ACTIVE_DELAY_MS = 1200;
export const WORKER_OVERVIEW_REFRESH_MS = 15000;
const ERROR_BASE_DELAY_MS = 5000;
const ERROR_MAX_DELAY_MS = 60000;

/** Keep active deployments responsive without polling an idle Worker every few seconds. */
export function nextWorkerPollDelay(hasPendingWork: boolean, consecutiveFailures: number): number {
  if (consecutiveFailures > 0) {
    return Math.min(
      ERROR_MAX_DELAY_MS,
      ERROR_BASE_DELAY_MS * 2 ** Math.min(consecutiveFailures - 1, 8),
    );
  }
  return hasPendingWork ? ACTIVE_DELAY_MS : WORKER_OVERVIEW_REFRESH_MS;
}

export type WorkerActivity = { active: boolean; token: string };

export async function pollWorkerOverview<T>(
  previous: WorkerActivity | undefined,
  readActivity: () => Promise<WorkerActivity>,
  readOverview: () => Promise<T>,
): Promise<T | undefined> {
  if (previous?.active) {
    try {
      const activity = await readActivity();
      if (activity.token === previous.token) return undefined;
    } catch (error) {
      // Older control-plane replicas may not have the activity route yet.
      if (!(error instanceof APIError) || error.status !== 404) throw error;
    }
  }
  return readOverview();
}
