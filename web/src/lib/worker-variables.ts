import { t } from "./i18n.ts";

export type VariableRow = {
  id: string;
  name: string;
  kind: "text" | "json";
  value: string;
};

export function variableRows(variables: Record<string, unknown> = {}): VariableRow[] {
  return Object.entries(variables).map(([name, value]) => ({
    id: crypto.randomUUID(),
    name,
    kind: typeof value === "string" ? "text" : "json",
    value: typeof value === "string" ? value : JSON.stringify(value, null, 2),
  }));
}

export function variableInput(rows: VariableRow[]): Record<string, unknown> {
  const variables: Record<string, unknown> = Object.create(null);
  for (const row of rows) {
    const name = row.name.trim();
    if (!/^[A-Za-z_$][A-Za-z0-9_$]{0,127}$/.test(name) || name.startsWith("KRUN_")) {
      throw new Error(t("变量名须为有效的 JavaScript 标识符，且不能使用 KRUN_ 前缀。"));
    }
    if (Object.hasOwn(variables, name)) {
      throw new Error(t("变量名称不能重复。"));
    }
    if (row.kind === "text") {
      variables[name] = row.value;
    } else {
      try {
        variables[name] = JSON.parse(row.value);
      } catch {
        throw new Error(t("变量 {{name}} 的值不是有效的 JSON。", { name }));
      }
    }
  }
  return variables;
}
