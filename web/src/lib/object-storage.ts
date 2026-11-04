import { t } from "./i18n";

export type WorkerBucket = {
  id: string;
  organization_id: string;
  name: string;
  enabled: boolean;
  revision: number;
  created_at: string;
};
export type R2Binding = { bucket_id: string; bucket_name: string };
export type R2BindingRow = { id: string; name: string; bucket: string };

export function bindingRows(bindings: Record<string, R2Binding> = {}): R2BindingRow[] {
  return Object.entries(bindings).map(([name, bucket]) => ({
    id: crypto.randomUUID(),
    name,
    bucket: bucket.bucket_name,
  }));
}

export function bindingInput(
  rows: R2BindingRow[],
  variables: unknown,
  secrets: string[],
  metadata: string,
) {
  const used = new Set([
    ...Object.keys(variables || {}),
    ...secrets,
    ...(metadata ? [metadata] : []),
  ]);
  const result: Record<string, string> = Object.create(null);
  for (const row of rows) {
    const name = row.name.trim();
    if (
      !/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name) ||
      name.startsWith("KRUN_") ||
      ["__proto__", "prototype", "constructor"].includes(name)
    ) {
      throw new Error(
        t("R2 绑定名称须为 1–64 位字母、数字或下划线，以字母或下划线开头，不能使用 KRUN_ 前缀。"),
      );
    }
    if (used.has(name)) {
      throw new Error(t("R2 绑定名称重复，或与变量、Secret、版本元数据冲突。"));
    }
    if (!row.bucket) {
      throw new Error(t("请为每个 R2 绑定选择一个桶。"));
    }
    used.add(name);
    result[name] = row.bucket;
  }
  return result;
}
