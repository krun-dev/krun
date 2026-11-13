import { t } from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type ReactNode } from "react";
import { LoaderCircle, Moon, ShieldCheck, Sun, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function Logo({ dark = false }: { dark?: boolean }) {
  return (
    <div
      className="flex items-center gap-2.5"
      data-theme={dark ? "dark" : "light"}
    >
      <img
        src="/brand/krun-symbol-yellow.png"
        alt=""
        aria-hidden="true"
        className="size-8 shrink-0 object-contain"
      />
      <span className="text-xl font-semibold tracking-tight">krun</span>
    </div>
  );
}
export function ThemeToggle({ className = "" }: { className?: string }) {
  useTranslation();
  const [theme, setTheme] = useState<"light" | "dark">(() =>
    document.documentElement.classList.contains("dark") ? "dark" : "light",
  );

  useEffect(() => {
    const syncTheme = () => {
      setTheme(document.documentElement.classList.contains("dark") ? "dark" : "light");
    };
    window.addEventListener("krun-theme-change", syncTheme);
    return () => window.removeEventListener("krun-theme-change", syncTheme);
  }, []);

  function toggleTheme() {
    const nextTheme = theme === "light" ? "dark" : "light";
    document.documentElement.classList.toggle("dark", nextTheme === "dark");
    setTheme(nextTheme);
    try {
      localStorage.setItem("krun.theme", nextTheme);
    } catch {
      /* Theme still works without storage. */
    }
    window.dispatchEvent(new Event("krun-theme-change"));
  }

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      className={className}
      aria-label={t(theme === "dark" ? "切换浅色主题" : "切换深色主题")}
      onClick={toggleTheme}
    >
      {theme === "dark" ? <Sun size={16} /> : <Moon size={16} />}
    </Button>
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
      {hint && <span className="text-xs font-normal leading-4 text-muted-foreground">{hint}</span>}
    </Label>
  );
}
export function SelectField({
  value,
  onChange,
  children,
  label,
  ariaLabel,
  hideLabel = false,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  children: ReactNode;
  label?: string;
  ariaLabel?: string;
  hideLabel?: boolean;
  disabled?: boolean;
}) {
  useTranslation();
  const select = (
    <select
      aria-label={ariaLabel ?? label}
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
  if (!label || hideLabel) return select;
  return (
    <span className="flex flex-col items-start gap-2 self-start text-sm font-medium">
      <span className="leading-5">{label}</span>
      {select}
    </span>
  );
}
export function Empty({
  title = t("暂无数据"),
  description,
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
      {description && <p className="max-w-md text-sm text-muted-foreground">{description}</p>}
      {action}
    </div>
  );
}
export function Loading() {
  useTranslation();
  return (
    <>
      <ThemeToggle className="fixed right-5 top-4" />
      <div
        role="status"
        className="flex min-h-screen items-center justify-center gap-2 text-muted-foreground"
      >
        <LoaderCircle
          className="animate-spin"
          size={18}
        />
        {t("加载中…")}
      </div>
    </>
  );
}
export function PageHeader({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  useTranslation();
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-medium tracking-tight">{title}</h1>
        {description && <p className="mt-2 text-sm text-muted-foreground">{description}</p>}
      </div>
      {action}
    </div>
  );
}
export function StatusBadge({ active }: { active: boolean }) {
  useTranslation();
  return (
    <Badge
      variant="outline"
      className={active ? "border-primary/25 bg-primary/10 text-primary" : "text-muted-foreground"}
    >
      <span className={cn("mr-1 size-1.5 rounded-full", active ? "bg-primary" : "bg-gray-400")} />
      {active ? t("有效") : t("已停用")}
    </Badge>
  );
}
export function Notice({ message, close }: { message: string; close: () => void }) {
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
