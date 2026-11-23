import { useEffect, useState } from "react";
import { api, APIError } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";

export type Propagation = {
  target_generation: number;
  state: "current" | "pending" | "unknown";
  membership_confirmed: boolean;
  total: number;
  applied: number;
  unsubscribed: number;
  lagging: number;
  unknown: number;
  rejected: number;
};

export function PropagationStatus({ base, environment }: { base: string; environment: string }) {
  const [value, setValue] = useState<Propagation | null>(null);
  const [error, setError] = useState("");
  const [legacy, setLegacy] = useState(false);
  useEffect(() => {
    let disposed = false;
    let loading = false;
    setValue(null);
    setError("");
    setLegacy(false);
    async function refresh() {
      if (loading) return;
      loading = true;
      try {
        const data = await api<Propagation>(
          base + "/environments/" + encodeURIComponent(environment) + "/propagation",
        );
        if (!disposed) {
          setValue(data);
          setError("");
        }
      } catch (e) {
        if (!disposed) {
          if (e instanceof APIError && e.code === "CONFIG_PROTOCOL_REQUIRED") setLegacy(true);
          else setError(errorMessage(e));
        }
      } finally {
        loading = false;
      }
    }
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, [base, environment]);
  if (legacy) return null;
  return (
    <section
      className="mt-4 rounded-md border p-3 text-sm"
      aria-label={t("配置传播状态")}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="font-medium">{t("配置传播状态")}</h4>
        <Badge variant="outline">
          {value
            ? t(
                value.state === "current"
                  ? "当前成员已确认"
                  : value.state === "pending"
                    ? "配置传播中"
                    : "传播状态未知",
              )
            : t("正在查询")}
        </Badge>
      </div>
      {error ? (
        <p
          className="mt-2 text-destructive"
          role="alert"
        >
          {error}
        </p>
      ) : null}
      {value ? (
        <div
          className="mt-2 grid gap-2"
          aria-live="polite"
        >
          <p>
            {t("目标配置代次")}：<code>{value.target_generation}</code>
          </p>
          <dl className="grid grid-cols-2 gap-2 sm:grid-cols-3">
            {(
              [
                ["已应用", value.applied],
                ["尚未缓存", value.unsubscribed],
                ["待同步", value.lagging],
                ["未知成员", value.unknown],
                ["拒绝应用", value.rejected],
                ["观测成员", value.total],
              ] as const
            ).map(([label, count]) => (
              <div
                key={label}
                className="flex items-center justify-between gap-2 rounded bg-muted/40 px-2 py-1"
              >
                <dt className="text-muted-foreground">{t(label)}</dt>
                <dd>{count}</dd>
              </div>
            ))}
          </dl>
          <p className="text-xs text-muted-foreground">
            {t("发布可用与配置传播分别确认；断连成员保留已知配置，状态显示为未知。")}
          </p>
          {!value.membership_confirmed ? (
            <p className="text-xs text-muted-foreground">
              {t("成员清单尚未确认，不能据此判定全池收敛。")}
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
