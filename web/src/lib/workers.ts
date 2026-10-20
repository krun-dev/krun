import { useEffect, useRef, useState } from "react";
import { api, type List, type Resource } from "./api";
import { errorMessage } from "./i18n";

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
  environments: Pick<
    Environment,
    "name" | "enabled" | "deployment_id" | "confirmed_deployment_id"
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
  host: string;
  configuration_revision: number;
  variables: Record<string, string>;
  release_revision: number;
  deployment_id?: string;
  confirmed_deployment_id?: string;
};
export type Deployment = {
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
  deployment_id: string;
  build_id?: string;
  error?: string;
  created_at: string;
  steps: { name: string; state: string; detail?: string }[];
};
export type History = {
  revision: number;
  deployment_id: string;
  operation_id: string;
  kind: string;
  time: string;
};
export type WorkerData = {
  builds: Build[];
  environments: Environment[];
  operations: Operation[];
  histories: Record<string, History[]>;
  deployments: Record<string, Deployment>;
};
export const pending = (state: string) =>
  ["queued", "fetching", "building", "validating", "committed"].includes(state);
export const shortID = (id?: string) => (id ? id.slice(0, 16) : "—");

export function useWorkerData(base: string, revision: number) {
  const [data, setData] = useState<WorkerData | null>(null);
  const [error, setError] = useState("");
  const cache = useRef<Record<string, Deployment>>({});
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      let delay = 5000;
      try {
        const [b, e, o] = await Promise.all([
          api<List<Build>>(
            base + "/builds",
            "GET",
            undefined,
            controller.signal,
          ),
          api<List<Environment>>(
            base + "/environments",
            "GET",
            undefined,
            controller.signal,
          ),
          api<List<Operation>>(
            base + "/operations",
            "GET",
            undefined,
            controller.signal,
          ),
        ]);
        const histories = Object.fromEntries(
          await Promise.all(
            e.items.map(
              async (env) =>
                [
                  env.name,
                  (
                    await api<List<History>>(
                      base + "/environments/" + env.name + "/history",
                      "GET",
                      undefined,
                      controller.signal,
                    )
                  ).items,
                ] as const,
            ),
          ),
        );
        const ids = new Set(
          e.items
            .flatMap((env) => [
              env.deployment_id,
              env.confirmed_deployment_id,
              ...histories[env.name].map((h) => h.deployment_id),
            ])
            .filter((id): id is string => !!id),
        );
        const fresh = await Promise.all(
          [...ids]
            .filter((id) => !cache.current[id])
            .map((id) =>
              api<Deployment>(
                base + "/deployments/" + id,
                "GET",
                undefined,
                controller.signal,
              ),
            ),
        );
        if (controller.signal.aborted) return;
        for (const d of fresh) cache.current[d.id] = d;
        setData({
          builds: b.items,
          environments: e.items,
          operations: o.items,
          histories,
          deployments: { ...cache.current },
        });
        setError("");
        if (
          b.items.some((b) => pending(b.state)) ||
          o.items.some((o) => pending(o.state))
        )
          delay = 1200;
      } catch (e) {
        if (!controller.signal.aborted) setError(errorMessage(e));
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(load, delay);
      }
    }
    void load();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [base, revision]);
  return { data, error };
}
