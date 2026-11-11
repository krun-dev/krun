import { bindingInput, bindingRows, type R2Binding, type R2BindingRow } from "./object-storage.ts";
import { variableInput, variableRows, type VariableRow } from "./worker-variables.ts";

export type HyperdriveBindingRow = { name: string; id: string };
export type ConfigSection = "variables" | "r2" | "hyperdrive";

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

export function environmentConfigPatch(
  section: ConfigSection,
  saved: ConfigDraftSource,
  draft: Omit<ConfigDraftRows, "secretNames">,
) {
  switch (section) {
    case "variables":
      return { variables: variableInput(draft.variables), version_metadata: draft.versionMetadata };
    case "r2":
      return {
        r2_buckets: bindingInput(
          draft.r2Bindings,
          saved.variables,
          saved.secret_names || [],
          saved.version_metadata || "",
        ),
      };
    case "hyperdrive":
      return { hyperdrive_bindings: hyperdriveInput(draft.hyperdriveBindings) };
  }
}
