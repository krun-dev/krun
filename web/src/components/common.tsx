import { t } from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import {
  ArrowUpRight,
  LoaderCircle,
  ShieldCheck,
  Terminal,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function Logo({ dark = false }: { dark?: boolean }) {
  useTranslation();
  return (
    <div className="flex items-center gap-2.5">
      <div
        className={cn(
          "flex size-8 items-center justify-center rounded-md border border-primary/20 bg-primary/10 text-primary",
          dark && "bg-card/10",
        )}
      >
        <Terminal size={18} />
      </div>
      <span className="text-xl font-semibold tracking-tight">
        krun<span className="text-primary">.</span>
      </span>
    </div>
  );
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  useTranslation();
  return (
    <Label className="grid items-start gap-2 text-sm font-medium">
      <span className="leading-5">{label}</span>
      {children}
      {hint && (
        <span className="text-xs font-normal leading-4 text-muted-foreground">
          {hint}
        </span>
      )}
    </Label>
  );
}
export function SelectField({
  value,
  onChange,
  children,
  label,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  children: ReactNode;
  label?: string;
  disabled?: boolean;
}) {
  useTranslation();
  const select = (
    <select
      aria-label={label}
      className="h-9 w-full rounded-md border bg-background px-3 text-sm outline-none focus:ring-2 focus:ring-primary/20"
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
    >
      {children}
    </select>
  );
  // A bare select misaligns next to Field inputs inside grids; wrap with the
  // same label structure when a label is provided.
  if (!label) return select;
  return (
    <span className="flex flex-col items-start gap-2 self-start text-sm font-medium">
      <span className="leading-5">{label}</span>
      {select}
    </span>
  );
}
export function Empty({
  title = t("暂无数据"),
  description = t("创建第一条记录，开始管理你的组织。"),
  action,
}: {
  title?: string;
  description?: string;
  action?: ReactNode;
}) {
  useTranslation();
  return (
    <div className="flex min-h-52 flex-col items-center justify-center gap-3 rounded-lg border border-dashed p-8 text-center">
      <div className="rounded-lg bg-secondary p-3 text-muted-foreground">
        <ShieldCheck size={22} />
      </div>
      <h3 className="font-medium">{title}</h3>
      <p className="max-w-md text-sm text-muted-foreground">{description}</p>
      {action}
    </div>
  );
}
export function Loading() {
  useTranslation();
  return (
    <div
      role="status"
      className="flex min-h-52 items-center justify-center gap-2 text-muted-foreground"
    >
      <LoaderCircle className="animate-spin" size={18} />
      {t("加载中…")}
    </div>
  );
}
export function PageHeader({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow: string;
  title: string;
  description: string;
  action?: ReactNode;
}) {
  useTranslation();
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <p className="mb-2 text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
          {eyebrow}
        </p>
        <h1 className="text-2xl font-medium tracking-tight">{title}</h1>
        <p className="mt-2 text-sm text-muted-foreground">{description}</p>
      </div>
      {action}
    </div>
  );
}
export function Metric({
  label,
  value,
  icon,
  note,
}: {
  label: string;
  value: ReactNode;
  icon: ReactNode;
  note: string;
}) {
  useTranslation();
  return (
    <Card className="gap-0 py-0 shadow-none">
      <CardContent className="p-5">
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          {label}
          {icon}
        </div>
        <div className="my-3 text-3xl font-semibold tracking-tight">
          {value}
        </div>
        <p className="text-xs text-muted-foreground">{note}</p>
      </CardContent>
    </Card>
  );
}
export function StatusBadge({ active }: { active: boolean }) {
  useTranslation();
  return (
    <Badge
      variant="outline"
      className={
        active
          ? "border-primary/25 bg-primary/10 text-primary"
          : "text-muted-foreground"
      }
    >
      <span
        className={cn(
          "mr-1 size-1.5 rounded-full",
          active ? "bg-primary" : "bg-gray-400",
        )}
      />
      {active ? t("有效") : t("已停用")}
    </Badge>
  );
}
export function Notice({
  message,
  close,
}: {
  message: string;
  close: () => void;
}) {
  useTranslation();
  return (
    <div
      role="status"
      className="mb-5 flex items-center justify-between gap-4 rounded-lg border border-primary/20 bg-primary/5 px-4 py-3 text-sm"
    >
      <span>{t(message)}</span>
      <Button
        size="icon"
        variant="ghost"
        aria-label={t("关闭消息")}
        onClick={close}
      >
        <X size={16} />
      </Button>
    </div>
  );
}
export function QuickLink({
  title,
  description,
  onClick,
}: {
  title: string;
  description: string;
  onClick: () => void;
}) {
  useTranslation();
  return (
    <button
      onClick={onClick}
      className="flex w-full items-center justify-between rounded-lg border p-4 text-left transition hover:border-primary/30 hover:bg-secondary/40"
    >
      <div>
        <p className="text-sm font-medium">{title}</p>
        <p className="mt-1 text-xs text-muted-foreground">{description}</p>
      </div>
      <ArrowUpRight size={16} className="text-muted-foreground" />
    </button>
  );
}
