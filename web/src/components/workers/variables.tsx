import { t } from "@/lib/i18n";
import type { VariableRow } from "@/lib/worker-variables";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Field, SelectField } from "@/components/common";

export function VariablesEditor({
  rows,
  onChange,
  disabled,
}: {
  rows: VariableRow[];
  onChange: (rows: VariableRow[]) => void;
  disabled: boolean;
}) {
  function update(id: string, value: Partial<VariableRow>) {
    onChange(rows.map((row) => (row.id === id ? { ...row, ...value } : row)));
  }

  return (
    <div className="grid gap-3">
      <p className="text-xs text-muted-foreground">
        {t(
          "给 Worker 代码使用的普通配置，例如 API_URL。代码通过 env.API_URL 读取；密码和 Token 请放在 Secrets 中。",
        )}
      </p>
      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("尚未配置环境变量。")}</p>
      ) : null}
      {rows.map((row, index) => (
        <div
          key={row.id}
          className="grid items-start gap-3 rounded-md border p-3 sm:grid-cols-[1fr_2fr_auto]"
        >
          <Field label={t("变量名")}>
            <Input
              aria-label={t("变量名 {{index}}", { index: index + 1 })}
              placeholder="API_URL"
              value={row.name}
              disabled={disabled}
              onChange={(event) => update(row.id, { name: event.target.value })}
            />
          </Field>
          <div className="grid gap-3">
            <Field label={t("变量值")}>
              <Textarea
                aria-label={t("变量值 {{index}}", { index: index + 1 })}
                placeholder={row.kind === "text" ? "https://api.example.com" : '{"enabled": true}'}
                value={row.value}
                rows={2}
                disabled={disabled}
                onChange={(event) => update(row.id, { value: event.target.value })}
              />
            </Field>
            <SelectField
              label={t("值类型")}
              ariaLabel={t("值类型 {{index}}", { index: index + 1 })}
              value={row.kind}
              disabled={disabled}
              onChange={(kind) => update(row.id, { kind: kind as VariableRow["kind"] })}
            >
              <option value="text">{t("文本（常用）")}</option>
              <option value="json">{t("JSON（对象、数组、数字等）")}</option>
            </SelectField>
          </div>
          <Button
            className="sm:mt-7"
            variant="outline"
            disabled={disabled}
            aria-label={t("移除变量 {{index}}", { index: index + 1 })}
            onClick={() => onChange(rows.filter((item) => item.id !== row.id))}
          >
            {t("移除")}
          </Button>
        </div>
      ))}
      <div>
        <Button
          variant="outline"
          disabled={disabled || rows.length >= 32}
          onClick={() =>
            onChange([...rows, { id: crypto.randomUUID(), name: "", value: "", kind: "text" }])
          }
        >
          {t("添加变量")}
        </Button>
      </div>
    </div>
  );
}

export function VersionMetadataField({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  return (
    <details className="rounded-md border p-3">
      <summary className="cursor-pointer text-sm font-medium">
        {t("高级设置：在代码中读取版本信息")}
      </summary>
      <div className="mt-3 grid gap-3">
        <p className="text-xs text-muted-foreground">
          {t("可选。设置一个变量名，Worker 就能读取当前版本的 ID、标签和创建时间；不需要时留空。")}
        </p>
        <Field
          label={t("版本信息变量名")}
          hint={t(
            "例如填入 VERSION_INFO，代码通过 env.VERSION_INFO.id 读取版本 ID。这里填写名称，不是版本号。",
          )}
        >
          <Input
            aria-label={t("版本信息变量名")}
            value={value}
            placeholder="VERSION_INFO"
            disabled={disabled}
            onChange={(event) => onChange(event.target.value)}
          />
        </Field>
      </div>
    </details>
  );
}
