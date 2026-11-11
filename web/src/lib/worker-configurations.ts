import type { Environment, Version } from "./workers";

export type WorkerConfiguration = Pick<
  Environment,
  | "variables"
  | "r2_buckets"
  | "hyperdrive_bindings"
  | "version_metadata"
  | "assets_binding"
  | "secret_names"
> & {
  id: string;
  worker_id: string;
  organization_id: string;
  environment: string;
  name: string;
  message?: string;
  created_at: string;
};

export type ConfigurationValues = Pick<
  Environment,
  "variables" | "r2_buckets" | "hyperdrive_bindings" | "version_metadata" | "assets_binding"
> & { secret_names?: string[]; secret_digest?: string };

function stableValue(value: unknown): string {
  if (Array.isArray(value)) return "[" + value.map(stableValue).join(",") + "]";
  if (value && typeof value === "object") {
    return (
      "{" +
      Object.entries(value)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([key, item]) => JSON.stringify(key) + ":" + stableValue(item))
        .join(",") +
      "}"
    );
  }
  return JSON.stringify(value) ?? "";
}

export function configurationDifferences(
  selected: ConfigurationValues,
  current?: ConfigurationValues,
) {
  const rows: { name: string; before: string; after: string; changed: boolean }[] = [];
  const add = (name: string, before: string, after: string) =>
    rows.push({ name, before, after, changed: before !== after });
  for (const name of [
    ...new Set([
      ...Object.keys(current?.variables || {}),
      ...Object.keys(selected.variables || {}),
    ]),
  ].sort()) {
    add(
      name,
      Object.hasOwn(current?.variables || {}, name) ? stableValue(current!.variables[name]) : "—",
      Object.hasOwn(selected.variables || {}, name) ? stableValue(selected.variables[name]) : "—",
    );
  }
  for (const name of [
    ...new Set([
      ...Object.keys(current?.r2_buckets || {}),
      ...Object.keys(selected.r2_buckets || {}),
    ]),
  ].sort()) {
    add(
      name + " · R2",
      current?.r2_buckets?.[name]?.bucket_name || "—",
      selected.r2_buckets?.[name]?.bucket_name || "—",
    );
  }
  for (const name of [
    ...new Set([
      ...Object.keys(current?.hyperdrive_bindings || {}),
      ...Object.keys(selected.hyperdrive_bindings || {}),
    ]),
  ].sort()) {
    add(
      name + " · Hyperdrive",
      current?.hyperdrive_bindings?.[name] || "—",
      selected.hyperdrive_bindings?.[name] || "—",
    );
  }
  add("version_metadata", current?.version_metadata || "—", selected.version_metadata || "—");
  add("assets_binding", current?.assets_binding || "—", selected.assets_binding || "—");
  return rows;
}

export function confirmedConfigurationVersion(env: Environment, versions: Record<string, Version>) {
  return env.confirmed_version_id ? versions[env.confirmed_version_id] : undefined;
}
