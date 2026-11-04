import { useEffect, useState } from "react";
import { api, type List } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import type { R2BindingRow, WorkerBucket } from "@/lib/object-storage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, SelectField } from "@/components/common";

export function R2BindingsEditor({
  organizationBase,
  rows,
  onChange,
  disabled = false,
}: {
  organizationBase: string;
  rows: R2BindingRow[];
  onChange: (rows: R2BindingRow[]) => void;
  disabled?: boolean;
}) {
  const [buckets, setBuckets] = useState<WorkerBucket[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    void api<List<WorkerBucket>>(
      organizationBase + "/workers/buckets",
      "GET",
      undefined,
      controller.signal,
    )
      .then((result) => {
        if (!controller.signal.aborted) {
          setBuckets(result.items);
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(cause));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [organizationBase, refresh]);
  function update(id: string, value: Partial<R2BindingRow>) {
    onChange(rows.map((row) => (row.id === id ? { ...row, ...value } : row)));
  }
  return (
    <section aria-label={t("R2 存储绑定")} className="grid gap-3 border-t pt-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{t("R2 存储绑定")}</h3>
        <Button
          variant="ghost"
          size="sm"
          disabled={loading}
          onClick={() => setRefresh((value) => value + 1)}
        >
          {t("刷新桶列表")}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        {t("选择本组织的桶，业务通过 env.绑定名称 访问。保存后在下一次发布时生效。")}
      </p>
      {error ? (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      ) : null}
      {!loading && !error && !buckets.some((bucket) => bucket.enabled) ? (
        <p className="text-sm text-muted-foreground">
          {t("没有可用的桶，请先到对象存储页面创建或启用桶。")}
        </p>
      ) : null}
      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("尚未配置 R2 绑定。")}</p>
      ) : null}
      {rows.map((row, index) => (
        <div
          key={row.id}
          className="grid items-end gap-3 rounded-md border p-3 sm:grid-cols-[1fr_1fr_auto]"
        >
          <Field label={t("绑定名称")}>
            <Input
              aria-label={t("R2 绑定名称 {{index}}", { index: index + 1 })}
              value={row.name}
              placeholder="BUCKET"
              disabled={disabled}
              onChange={(event) => update(row.id, { name: event.target.value })}
            />
          </Field>
          <SelectField
            label={t("R2 桶 {{index}}", { index: index + 1 })}
            value={row.bucket}
            disabled={disabled || loading}
            onChange={(bucket) => update(row.id, { bucket })}
          >
            <option value="">{t("选择桶")}</option>
            {row.bucket && !buckets.some((bucket) => bucket.name === row.bucket) ? (
              <option value={row.bucket}>
                {row.bucket} · {t("不可用")}
              </option>
            ) : null}
            {buckets.map((bucket) => (
              <option key={bucket.id} value={bucket.name} disabled={!bucket.enabled}>
                {bucket.name}
                {bucket.enabled ? "" : " · " + t("已停用")}
              </option>
            ))}
          </SelectField>
          <Button
            variant="outline"
            disabled={disabled}
            aria-label={t("移除 R2 绑定 {{index}}", { index: index + 1 })}
            onClick={() => onChange(rows.filter((item) => item.id !== row.id))}
          >
            {t("移除")}
          </Button>
        </div>
      ))}
      <div>
        <Button
          variant="outline"
          disabled={
            disabled || rows.length >= 100 || loading || !buckets.some((bucket) => bucket.enabled)
          }
          onClick={() => onChange([...rows, { id: crypto.randomUUID(), name: "", bucket: "" }])}
        >
          {t("添加 R2 绑定")}
        </Button>
      </div>
    </section>
  );
}
