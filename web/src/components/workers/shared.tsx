import { useTranslation } from "react-i18next";
import { Check, Circle, Clock, History as HistoryIcon, LoaderCircle, Minus } from "lucide-react";
import { t } from "@/lib/i18n";
import { pending } from "@/lib/workers";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// Shared vocabulary for the Workers console: state labels, release-state icons
// and colours, and the public URL helper. Each panel imports only this module,
// so moving a tab into its own file never drags the whole page along.

export const states: Record<string, string> = {
  queued: "排队中",
  fetching: "拉取源码",
  building: "构建中",
  succeeded: "构建成功",
  failed: "失败",
  validating: "验证候选",
  committed: "已提交，生效中",
  available: "已生效",
  superseded: "已被替换",
};
export const artifactIcons: Record<string, typeof Check> = {
  当前: Check,
  历史: HistoryIcon,
  未发布: Circle,
  生效中: Clock,
  不可用: Minus,
};
// Release state color system (single source of truth):
//   当前   → primary (brand): the good, expected state
//   生效中 → amber: transient, worth the eye
//   历史   → muted: superseded, no attention needed
//   未发布/不可用 → no color at all
//   失败   → destructive (shared with buttons/errors)
export const deployColors: Record<string, string> = {
  当前: "text-primary",
  历史: "text-muted-foreground",
  未发布: "text-muted-foreground/60",
  生效中: "text-amber-600 dark:text-amber-500",
  不可用: "text-muted-foreground/60",
};
export const artifactColors = deployColors;
export const stepStates: Record<string, string> = {
  pending: "等待执行",
  running: "处理中",
  done: "已完成",
  failed: "失败",
};
export const stepNames: Record<string, string> = {
  artifact: "制品检查",
  candidate: "候选验证",
  commit: "提交 Release",
  synchronize: "运行副本确认",
};
export const idempotency = () => crypto.randomUUID();

// Always the production form: the plain domain over HTTPS. Local tunnels
// (ports, Host headers) are a developer concern and stay out of the URL.
export function workerURL(host: string): string {
  return "https://" + host + "/";
}

export function Status({ state }: { state: string }) {
  useTranslation();
  return (
    <Badge
      variant="outline"
      className={cn(
        "gap-1",
        state === "failed"
          ? "text-destructive"
          : state === "available" || state === "succeeded"
            ? "text-primary"
            : "text-muted-foreground",
      )}
    >
      {pending(state) ? (
        <LoaderCircle size={12} className="animate-spin" />
      ) : null}
      {t(states[state] || state)}
    </Badge>
  );
}
