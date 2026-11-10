import { api } from "./api";

const returnKey = "krun.repository-return";

type RepositoryReturn = {
  key: string;
  workspace: string;
  platformWorkspace?: string;
  page: "workers" | "git-authorizations";
  worker: string;
  environment: string;
};

export function repositoryEndpoint(organizationBase: string, provider: string) {
  return organizationBase + "/worker-repositories/" + encodeURIComponent(provider);
}

export async function connectRepository(organizationBase: string, provider: string) {
  const result = await api<{ authorization_url: string; resume_key: string }>(
    repositoryEndpoint(organizationBase, provider) + "/connect",
    "POST",
  );
  const params = new URLSearchParams(location.search);
  const inventoryOrg =
    params.get("view") === "system" && params.get("page") === "workers"
      ? params.get("inventory_org")
      : null;
  const workspace = params.get("workspace") || "";
  const target: RepositoryReturn = {
    key: result.resume_key,
    workspace: inventoryOrg || workspace,
    platformWorkspace: inventoryOrg ? workspace : undefined,
    page: params.get("page") === "git-authorizations" ? "git-authorizations" : "workers",
    worker: params.get(inventoryOrg ? "inventory_resource" : "worker") || "",
    environment: params.get("environment") || "",
  };
  try {
    sessionStorage.setItem(returnKey, JSON.stringify(target));
  } catch {
    // The server callback still returns to the Workers list without storage.
  }
  location.assign(result.authorization_url);
}

export function restoreRepositoryReturn() {
  const url = new URL(location.href);
  const key = url.searchParams.get("repository_resume");
  if (!key) return;
  url.searchParams.delete("repository_resume");
  try {
    const target: RepositoryReturn | null = JSON.parse(sessionStorage.getItem(returnKey) || "null");
    if (target?.key === key) {
      sessionStorage.removeItem(returnKey);
      const workspace = url.searchParams.get("workspace");
      if (workspace === target.workspace || !workspace) {
        url.searchParams.set(
          "page",
          target.page === "git-authorizations" ? target.page : "workers",
        );
        if (target.workspace) url.searchParams.set("workspace", target.workspace);
        if (target.page === "workers" && target.worker) {
          if (target.platformWorkspace !== undefined) {
            if (target.platformWorkspace)
              url.searchParams.set("workspace", target.platformWorkspace);
            else url.searchParams.delete("workspace");
            url.searchParams.set("view", "system");
            url.searchParams.set("inventory_org", target.workspace);
            url.searchParams.set("inventory_resource", target.worker);
          } else {
            url.searchParams.set("worker", target.worker);
          }
          url.searchParams.set("worker_tab", "source");
          if (target.environment) url.searchParams.set("environment", target.environment);
          const provider = url.searchParams.get("repository_provider");
          if (provider && url.searchParams.get("repository_status") === "connected") {
            url.searchParams.set("repository_pick", provider);
          }
        }
      }
    }
  } catch {
    // Ignore unavailable storage or an interrupted authorization flow.
  }
  history.replaceState(null, "", url);
}
