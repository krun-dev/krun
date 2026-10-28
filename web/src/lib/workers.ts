import { useEffect, useRef, useState } from "react";
import { api, type Resource } from "./api";
import { errorMessage } from "./i18n";
import { nextWorkerPollDelay } from "./worker-polling";

export type RepositoryBinding = {
  source: RepositorySource | null;
  revision: number;
};
export type RepositorySource = {
  provider_id: string;
  repository: string;
  ref: string;
  root_directory: string;
  entrypoint: string;
};
export type RepositoryProvider = {
  id: string;
  type: "github" | "gitlab";
  name: string;
  priority: number;
  base_url: string;
  connected: boolean;
  oauth: boolean;
};
export type WorkerServiceSummary = Resource & {
  repository_source?: RepositorySource;
  network_profile: WorkerNetworkProfile;
  environments: Pick<
    Environment,
    "name" | "enabled" | "release_id" | "confirmed_release_id" | "domains" | "hosts"
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
  name: string;
  enabled: boolean;
  /** Selected domain ids from the platform catalog. */
  domains: string[];
  /** One route host per selected domain, in the same order. */
  hosts: string[];
  configuration_revision: number;
  variables: Record<string, string>;
  secret_names: string[];
  release_revision: number;
  release_id?: string;
  confirmed_release_id?: string;
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
export type Release = {
  id: string;
  artifact_id: string;
  environment: string;
  variables: Record<string, string>;
  configuration_revision: number;
  archive_sha256: string;
};
export type Operation = {
  id: string;
  kind: string;
  state: string;
  environment: string;
  release_id: string;
  build_id?: string;
  error?: string;
  created_at: string;
  steps: { name: string; state: string; detail?: string }[];
};
export type History = {
  revision: number;
  release_id: string;
  operation_id: string;
  kind: string;
  time: string;
};
export type WorkerData = {
  builds: Build[];
  environments: Environment[];
  operations: Operation[];
  histories: Record<string, History[]>;
  releases: Record<string, Release>;
  domainsRevision: number;
};
type WorkerOverview = {
  builds: Build[];
  environments: {
    environment: Environment;
    history: History[];
    current_release?: Release;
    confirmed_release?: Release;
  }[];
  operations: Operation[];
  releases: Record<string, Release>;
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
    let timer: ReturnType<typeof setTimeout> | undefined;
    let request: AbortController | undefined;

    if (previousBase.current !== base) {
      previousBase.current = base;
      setData(null);
      setError("");
    }

    function schedule(delay: number) {
      if (timer) clearTimeout(timer);
      if (!disposed && !document.hidden) timer = setTimeout(load, delay);
    }

    async function load() {
      if (disposed || document.hidden || inFlight) return;
      inFlight = true;
      const currentRequest = new AbortController();
      request = currentRequest;
      let hasPendingWork = false;
      try {
        const overview = await api<WorkerOverview>(
          base + "/overview",
          "GET",
          undefined,
          currentRequest.signal,
        );
        if (disposed || currentRequest.signal.aborted) return;
        hasPendingWork =
          overview.builds.some((build) => pending(build.state)) ||
          overview.operations.some((operation) => pending(operation.state));
        setData({
          builds: overview.builds,
          environments: overview.environments.map((entry) => entry.environment),
          operations: overview.operations,
          histories: Object.fromEntries(
            overview.environments.map((entry) => [
              entry.environment.name,
              entry.history,
            ]),
          ),
          releases: overview.releases,
          domainsRevision: overview.domains_revision,
        });
        setError("");
        failures = 0;
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
          schedule(0);
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
      void load();
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
