import { bindingRows, type R2Binding, type R2BindingRow } from "./object-storage.ts";
import { variableRows, type VariableRow } from "./worker-variables.ts";

export type HyperdriveBindingRow = { name: string; id: string };

export type ConfigDraftSource = {
  variables: Record<string, unknown>;
  r2_buckets?: Record<string, R2Binding>;
  hyperdrive_bindings?: Record<string, string>;
  version_metadata?: string;
  secret_names?: string[];
};

export type ConfigDraftRows = {
  variables: VariableRow[];
  r2Bindings: R2BindingRow[];
  hyperdriveBindings: HyperdriveBindingRow[];
  versionMetadata: string;
  secretNames: string[];
};

export function hyperdriveRows(bindings?: Record<string, string>): HyperdriveBindingRow[] {
  return Object.entries(bindings || {}).map(([name, id]) => ({ name, id }));
}

export function hyperdriveInput(rows: HyperdriveBindingRow[]): Record<string, string> {
  return Object.fromEntries(
    rows.filter((row) => row.name.trim() && row.id).map((row) => [row.name.trim(), row.id]),
  );
}

export function draftRows(source: ConfigDraftSource): ConfigDraftRows {
  return {
    variables: variableRows(source.variables),
    r2Bindings: bindingRows(source.r2_buckets),
    hyperdriveBindings: hyperdriveRows(source.hyperdrive_bindings),
    versionMetadata: source.version_metadata || "",
    secretNames: source.secret_names || [],
  };
}
