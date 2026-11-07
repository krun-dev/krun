import { useEffect, useState } from "react";
import { api, APIError } from "@/lib/api";
import { errorMessage } from "@/lib/i18n";
import type { RepositoryProvider } from "@/lib/workers";

export function useRepositoryProviders(organizationBase: string) {
  const [providers, setProviders] = useState<RepositoryProvider[] | null>(null);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setProviders(null);
    setError("");
    void api<{ items: RepositoryProvider[] }>(
      organizationBase + "/workers/repository-providers",
      "GET",
      undefined,
      controller.signal,
    )
      .then((result) => {
        if (!controller.signal.aborted) setProviders(result.items);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(error));
      });
    return () => controller.abort();
  }, [organizationBase, revision]);
  return { providers, setProviders, error, setError, reload: () => setRevision((n) => n + 1) };
}

export const repositoryStatusLabel = {
  not_connected: "未授权",
  connected: "已授权",
  refresh_due: "使用时续期",
  reconnect_required: "需要重新授权",
} as const;

export function repositoryAuthorizationError(error: unknown) {
  return error instanceof APIError && error.code === "WORKER_REPOSITORY_RECONNECT_REQUIRED";
}
