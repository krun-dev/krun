import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { t } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SelectField } from "@/components/common";
import type { HyperdriveConfig } from "@/components/hyperdrive";

export type HyperdriveBindingRow = { name: string; id: string };

export function HyperdriveBindingsEditor({
  organizationBase,
  rows,
  onChange,
  onSave,
  disabled,
}: {
  organizationBase: string;
  rows: HyperdriveBindingRow[];
  onChange: (rows: HyperdriveBindingRow[]) => void;
  onSave: () => void;
  disabled: boolean;
}) {
  const [configs, setConfigs] = useState<HyperdriveConfig[] | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void api<{ items: HyperdriveConfig[] }>(
      organizationBase + "/hyperdrive",
      "GET",
      undefined,
      controller.signal,
    )
      .then((value) => setConfigs(value.items))
      .catch(() => {
        if (!controller.signal.aborted) setConfigs(null);
      });
    return () => controller.abort();
  }, [organizationBase]);

  if (!configs && rows.length === 0) return null;
  const valid =
    rows.every((row) => row.name.trim() && row.id) &&
    new Set(rows.map((row) => row.name.trim())).size === rows.length;
  function change(index: number, value: HyperdriveBindingRow) {
    onChange(rows.map((row, i) => (i === index ? value : row)));
  }

  return (
    <section
      aria-label={t("Hyperdrive 绑定")}
      className="grid gap-3 border-t pt-4"
    >
      <h3 className="text-sm font-medium">{t("Hyperdrive 绑定")}</h3>
      <p className="text-xs text-muted-foreground">
        {t("保存后在下一次发布时生效，不会修改正在运行的版本。")}
      </p>
      {rows.map((row, index) => (
        <div
          key={index}
          className="grid items-end gap-2 sm:grid-cols-[1fr_2fr_auto]"
        >
          <Input
            aria-label={t("绑定名称")}
            placeholder="DB"
            value={row.name}
            onChange={(event) => change(index, { ...row, name: event.target.value })}
            disabled={disabled}
          />
          <SelectField
            ariaLabel={t("Hyperdrive 配置")}
            value={row.id}
            onChange={(id) => change(index, { ...row, id })}
            disabled={disabled || configs === null}
          >
            <option value="">{t("选择 Hyperdrive 配置")}</option>
            {row.id && !configs?.some((config) => config.id === row.id) && (
              <option value={row.id}>{row.id}</option>
            )}
            {configs?.map((config) => (
              <option
                key={config.id}
                value={config.id}
              >
                {config.name}
              </option>
            ))}
          </SelectField>
          <Button
            variant="outline"
            disabled={disabled}
            onClick={() => onChange(rows.filter((_, i) => i !== index))}
          >
            {t("删除")}
          </Button>
        </div>
      ))}
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          disabled={disabled || !configs?.length || rows.length >= 32}
          onClick={() => onChange([...rows, { name: "", id: "" }])}
        >
          {t("添加 Hyperdrive 绑定")}
        </Button>
        <Button
          disabled={disabled || !valid}
          onClick={onSave}
        >
          {t("保存 Hyperdrive 绑定")}
        </Button>
      </div>
    </section>
  );
}
