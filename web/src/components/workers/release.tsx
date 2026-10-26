import { Check, Circle, LoaderCircle } from "lucide-react";
import { t } from "@/lib/i18n";
import { shortID, type Build, type Environment, type Operation, type WorkerData } from "@/lib/workers";
import { Card, CardContent } from "@/components/ui/card";
import { SelectField } from "@/components/common";
import { cn } from "@/lib/utils";
import { Status, stepNames, stepStates, states } from "./shared";

export function ReleaseSummary({
  build,
  data,
  onSelect,
}: {
  build: Build;
  data: WorkerData;
  onSelect: (name: string) => void;
}) {
  const stateOf = (env: Environment): string => {
    if (build.state !== "succeeded") return "不可用";
    const current = env.release_id
      ? data.releases[env.release_id]
      : undefined;
    const historical = (data.histories[env.name] || []).some(
      (h) => data.releases[h.release_id]?.artifact_id === build.artifact_id,
    );
    if (current?.artifact_id === build.artifact_id)
      return env.release_id !== env.confirmed_release_id ? "生效中" : "当前";
    return historical ? "历史" : "未发布";
  };
  const deployed = data.environments
    .filter((e) => e.enabled)
    .filter((e) => ["当前", "生效中", "历史"].includes(stateOf(e)));
  return (
    <div className="flex flex-wrap gap-1.5">
      {deployed.length === 0 ? (
        <span className="text-xs italic text-muted-foreground">
          {t("未部署到任何环境")}
        </span>
      ) : (
        deployed.map((e) => {
          const s = stateOf(e);
          return (
            <button
              key={e.name}
              onClick={() => onSelect(e.name)}
              title={e.name + " · " + t(s)}
              className={cn(
                "rounded-full border px-2.5 py-0.5 text-[11px] transition hover:outline hover:outline-1 hover:outline-primary",
                s === "当前"
                  ? "border-primary/40 bg-primary/5 text-primary"
                  : s === "生效中"
                    ? "border-amber-500/40 bg-amber-500/5 text-amber-600 dark:text-amber-500"
                    : "border-border text-muted-foreground",
              )}
            >
              {e.name}
            </button>
          );
        })
      )}
    </div>
  );
}


export function OperationPanel({
  operation: op,
  operations,
  select,
}: {
  operation: Operation;
  operations: Operation[];
  select: (id: string) => void;
}) {
  return (
    <Card className="py-0 shadow-none">
      <CardContent className="p-5">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="font-medium">
              {t("发布进度")} · {op.environment}
            </h3>
            <p className="mt-1 text-xs text-muted-foreground">
              {shortID(op.id)} · {op.kind}
            </p>
          </div>
          <Status state={op.state} />
        </div>
        <div className="grid gap-4 sm:grid-cols-4">
          {op.steps.map((s) => (
            <div key={s.name} className="border-l-2 pl-3">
              <div className="flex items-center gap-2 text-sm">
                {s.state === "done" ? (
                  <Check size={14} className="text-primary" />
                ) : s.state === "running" ? (
                  <LoaderCircle size={14} className="animate-spin" />
                ) : (
                  <Circle size={14} className="text-muted-foreground" />
                )}
                {t(stepNames[s.name] || s.name)}
              </div>
              <p className="mt-1 text-xs text-muted-foreground">
                {t(stepStates[s.state] || s.state)}
              </p>
            </div>
          ))}
        </div>
        {op.error ? (
          <p role="alert" className="mt-4 text-sm text-destructive">
            {op.error}
          </p>
        ) : null}
        <div className="mt-5">
          <SelectField
            label={t("查看发布操作")}
            value={op.id}
            onChange={select}
          >
            {operations.map((o) => (
              <option key={o.id} value={o.id}>
                {o.environment} · {o.kind} · {shortID(o.id)} ·{" "}
                {t(states[o.state] || o.state)}
              </option>
            ))}
          </SelectField>
        </div>
      </CardContent>
    </Card>
  );
}
