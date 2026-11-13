import { useEffect, useRef, useState } from "react";
import { api, type Resource } from "./api";
import { errorMessage } from "./i18n";
import {
  nextWorkerPollDelay,
  pollWorkerOverview,
  WORKER_OVERVIEW_REFRESH_MS,
  type WorkerActivity,
} from "./worker-polling";
import type { R2Binding } from "./object-storage";

export type RepositoryBinding = {
  source: RepositorySource | null;
  revision: number;
  authorization_status?: RepositoryProvider["authorization_status"];
};
export type RepositorySource = {
  provider_id: string;
  repository: string;
  ref: string;
  root_directory: string;
};
export type RepositoryProvider = {
  id: string;
  type: "github" | "gitlab";
  name: string;
  priority: number;
  base_url: string;
  connected: boolean;
  oauth: boolean;
  authorization_status: "not_connected" | "connected" | "refresh_due" | "reconnect_required";
  install_url?: string;
};
export type WorkerSummary = Resource & {
  repository_source?: RepositorySource;
  network_profile: WorkerNetworkProfile;
  environments: Pick<
    Environment,
    "name" | "enabled" | "version_id" | "confirmed_version_id" | "domains" | "hosts"
  >[];
};
export type Build = {
  repository_source?: RepositorySource & { commit?: string };
  id: string;
  state: string;
  environment: string;
  entrypoint: string;
  artifact_id?: string;
  error?: string;
  created_at: string;
  job: string;
};
export type Environment = {
  r2_buckets?: Record<string, R2Binding>;
  hyperdrive_bindings?: Record<string, string>;
  name: string;
  enabled: boolean;
  /** Selected domain ids from the platform catalog. */
  domains: string[];
  /** One route host per selected domain, in the same order. */
  hosts: string[];
  configuration_revision: number;
  variables: Record<string, unknown>;
  version_metadata?: string;
  assets_binding?: string;
  secret_names: string[];
  deployment_revision: number;
  version_id?: string;
  confirmed_version_id?: string;
  candidate_version?: string;
  candidate_weight?: number;
};

/** Platform-configured domain. label is a free-form note, not a routing concept. */
export type WorkerDomain = {
  id: string;
  domain: string;
  label: string;
  default: boolean;
};
export type WorkerNetworkProfile = {
  id: string;
  name: string;
  description: string;
  approval_required: boolean;
  default: boolean;
};
export type WorkerRouting = {
  selected: string[];
  catalog: WorkerDomain[];
  revision: number;
  network_profile: WorkerNetworkProfile;
};
export type Version = {
  secret_names?: string[];
  configuration_id?: string;
  created_by?: string;
  created_by_name?: string;
  r2_buckets?: Record<string, R2Binding>;
  hyperdrive_bindings?: Record<string, string>;
  id: string;
  artifact_id: string;
  environment: string;
  variables: Record<string, unknown>;
  version_metadata?: string;
  assets_binding?: string;
  secret_digest?: string;
  tag?: string;
  message?: string;
  configuration_revision: number;
  archive_sha256: string;
  created_at: string;
};
export type Operation = {
  id: string;
  kind: string;
  state: string;
  environment: string;
  version_id: string;
  build_id?: string;
  error?: string;
  created_at: string;
  steps: { name: string; state: string; detail?: string }[];
};
export type History = {
  revision: number;
  version_id: string;
  operation_id: string;
  kind: string;
  time: string;
};
export type WorkerData = {
  builds: Build[];
  environments: Environment[];
  operations: Operation[];
  histories: Record<string, History[]>;
  versions: Record<string, Version>;
  domainsRevision: number;
};
type WorkerOverview = {
  activity?: WorkerActivity;
  builds: Build[];
  environments: {
    environment: Environment;
    history: History[];
    current_version?: Version;
    confirmed_version?: Version;
  }[];
  operations: Operation[];
  versions: Record<string, Version>;
  domains_revision: number;
};
export const pending = (state: string) =>
  ["queued", "fetching", "building", "validating", "committed"].includes(state);
export const shortID = (id?: string) => (id ? id.slice(0, 16) : "—");

export function useWorkerData(base: string, revision: number) {
  const [data, setData] = useState<WorkerData | null>(null);
  const [error, setError] = useState("");
  const previousBase = useRef(base);
  useEffect(() => {
    let disposed = false;
    let inFlight = false;
    let failures = 0;
    let reloadAfterFlight = false;
    let hasPendingWork = false;
    let activity: WorkerActivity | undefined;
    let lastOverview = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let request: AbortController | undefined;

    if (previousBase.current !== base) {
      previousBase.current = base;
      setData(null);
      setError("");
    }

    function schedule(delay: number, forceOverview = false) {
      if (timer) clearTimeout(timer);
      if (!disposed && !document.hidden) {
        timer = setTimeout(() => void load(forceOverview), delay);
      }
    }

    async function load(forceOverview = false) {
      if (disposed || document.hidden || inFlight) return;
      inFlight = true;
      const currentRequest = new AbortController();
      request = currentRequest;
      try {
        // Keep a periodic full refresh for metadata changes that do not affect active work.
        const refreshOverview =
          forceOverview || performance.now() - lastOverview >= WORKER_OVERVIEW_REFRESH_MS;
        const signal = currentRequest.signal;
        const overview = await pollWorkerOverview(
          refreshOverview ? undefined : activity,
          () => api<WorkerActivity>(base + "/activity", "GET", undefined, signal),
          () => api<WorkerOverview>(base + "/overview", "GET", undefined, signal),
        );
        if (disposed || signal.aborted) return;
        setError("");
        failures = 0;
        if (!overview) return;

        lastOverview = performance.now();
        activity = overview.activity;
        hasPendingWork =
          activity?.active ??
          (overview.builds.some((build) => pending(build.state)) ||
            overview.operations.some((operation) => pending(operation.state)));
        setData({
          builds: overview.builds,
          environments: overview.environments.map((entry) => entry.environment),
          operations: overview.operations,
          histories: Object.fromEntries(
            overview.environments.map((entry) => [entry.environment.name, entry.history]),
          ),
          versions: overview.versions,
          domainsRevision: overview.domains_revision,
        });
      } catch (cause) {
        if (!disposed && !currentRequest.signal.aborted) {
          failures++;
          setError(errorMessage(cause));
        }
      } finally {
        inFlight = false;
        if (request === currentRequest) request = undefined;
        if (reloadAfterFlight && !document.hidden) {
          reloadAfterFlight = false;
          schedule(0, true);
        } else {
          schedule(nextWorkerPollDelay(hasPendingWork, failures));
        }
      }
    }

    function visibilityChanged() {
      if (document.hidden) {
        if (timer) clearTimeout(timer);
        request?.abort();
        return;
      }
      if (inFlight) {
        reloadAfterFlight = true;
        return;
      }
      void load(true);
    }

    document.addEventListener("visibilitychange", visibilityChanged);
    void load();
    return () => {
      disposed = true;
      document.removeEventListener("visibilitychange", visibilityChanged);
      request?.abort();
      if (timer) clearTimeout(timer);
    };
  }, [base, revision]);
  return { data, error };
}
