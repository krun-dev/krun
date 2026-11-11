import { ArrowUpRight } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";

export function WorkerMetrics({ base, environment }: { base: string; environment: string }) {
  const [dashboard, setDashboard] = useState<{ dashboard_url?: string } | null>(null);
  const [error, setError] = useState("");
  const [theme, setTheme] = useState(() =>
    document.documentElement.classList.contains("dark") ? "dark" : "light",
  );

  useEffect(() => {
    const controller = new AbortController();
    api<{ dashboard_url?: string }>(base + "/metrics", "GET", undefined, controller.signal)
      .then(setDashboard)
      .catch((reason) => {
        if (!controller.signal.aborted) setError(errorMessage(reason));
      });
    return () => controller.abort();
  }, [base]);

  useEffect(() => {
    const syncTheme = () => {
      setTheme(document.documentElement.classList.contains("dark") ? "dark" : "light");
    };
    window.addEventListener("krun-theme-change", syncTheme);
    return () => window.removeEventListener("krun-theme-change", syncTheme);
  }, []);

  if (error)
    return (
      <p
        role="alert"
        className="text-sm text-destructive"
      >
        {error}
      </p>
    );
  if (!dashboard)
    return (
      <p
        role="status"
        className="text-sm text-muted-foreground"
      >
        {t("正在加载…")}
      </p>
    );
  if (!dashboard.dashboard_url) {
    return <p className="text-sm text-muted-foreground">{t("尚未配置 Grafana 指标看板。")}</p>;
  }

  const url = new URL(dashboard.dashboard_url);
  url.searchParams.set("var-environment", environment);
  url.searchParams.set("theme", theme);
  const embed = new URL(url);
  embed.searchParams.set("kiosk", "1");

  return (
    <div className="grid gap-3">
      <Button
        asChild
        variant="outline"
        size="sm"
        className="justify-self-end"
      >
        <a
          href={url.href}
          target="_blank"
          rel="noreferrer"
        >
          {t("在 Grafana 中打开")}
          <ArrowUpRight aria-hidden="true" />
        </a>
      </Button>
      <iframe
        src={embed.href}
        title={t("Worker 指标")}
        referrerPolicy="no-referrer"
        className="h-[75vh] min-h-[32rem] w-full rounded-lg border bg-background"
      />
    </div>
  );
}
