import { useState } from "react";
import {
  draftRows,
  type ConfigDraftSource,
  type HyperdriveBindingRow,
} from "./worker-config-draft";
import type { R2BindingRow } from "./object-storage";
import type { VariableRow } from "./worker-variables";

export function useConfigDraft(source: ConfigDraftSource) {
  const initial = draftRows(source);
  const [variables, setVariables] = useState<VariableRow[]>(initial.variables);
  const [r2Bindings, setR2Bindings] = useState<R2BindingRow[]>(initial.r2Bindings);
  const [hyperdriveBindings, setHyperdriveBindings] = useState<HyperdriveBindingRow[]>(
    initial.hyperdriveBindings,
  );
  const [versionMetadata, setVersionMetadata] = useState(initial.versionMetadata);

  function reset(next: ConfigDraftSource) {
    const rows = draftRows(next);
    setVariables(rows.variables);
    setR2Bindings(rows.r2Bindings);
    setHyperdriveBindings(rows.hyperdriveBindings);
    setVersionMetadata(rows.versionMetadata);
  }

  return {
    variables,
    setVariables,
    r2Bindings,
    setR2Bindings,
    hyperdriveBindings,
    setHyperdriveBindings,
    versionMetadata,
    setVersionMetadata,
    reset,
  };
}
