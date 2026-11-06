import { t } from "@/lib/i18n";
import { configurationDifferences, type ConfigurationValues } from "@/lib/worker-configurations";
import {
  Table,
  TableHeader,
  TableHead,
  TableRow,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

export function ConfigurationDiff({
  selected,
  current,
}: {
  selected: ConfigurationValues;
  current?: ConfigurationValues;
}) {
  const rows = configurationDifferences(selected, current);
  return (
    <div className="grid gap-3">
      <div className="overflow-x-auto">
        <Table className="table-fixed text-xs">
          <TableHeader>
            <TableRow>
              <TableHead className="w-1/3">{t("配置项")}</TableHead>
              <TableHead>{t("当前生效")}</TableHead>
              <TableHead>{t("所选配置")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow
                key={row.name}
                className={cn(row.changed && "bg-amber-500/5")}
              >
                <TableCell className="whitespace-normal break-all font-mono">{row.name}</TableCell>
                <TableCell className="whitespace-pre-wrap break-all">{row.before}</TableCell>
                <TableCell className="whitespace-pre-wrap break-all">
                  {row.after}
                  {row.changed ? (
                    <span className="ml-1 text-muted-foreground">{t("（变更）")}</span>
                  ) : null}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <p className="break-all text-xs text-muted-foreground">
        {t("Secrets")} ·{" "}
        {selected.secret_names?.length
          ? selected.secret_names.join("、")
          : selected.secret_digest
            ? t("已固定 Secret 版本引用，值不可读取。")
            : t("无")}
      </p>
      <p className="text-xs text-muted-foreground">
        {t("高亮显示普通配置差异；Secrets 使用保存时的固定引用，不展示值或逐值比较。")}
      </p>
    </div>
  );
}
