import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import type { WorkerData, Environment } from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { shortID } from "@/lib/workers";

type Deployment = {
  id: string;
  version_id: string;
  environment: string;
  state: string;
  hosts: string[];
  expires_at: string;
};
export function VersionsPanel({
  base,
  env,
  data,
  reload,
  notify,
}: {
  base: string;
  env: Environment;
  data: WorkerData;
  reload: () => void;
  notify: (s: string) => void;
}) {
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let cancelled = false;
    void api<{ items: Deployment[] }>(base + "/deployments")
      .then((result) => {
        if (!cancelled) setDeployments(result.items);
      })
      .catch((e) => {
        if (!cancelled) notify(errorMessage(e));
      });
    return () => {
      cancelled = true;
    };
  }, [base, data, notify]);
  async function deploy(id: string) {
    setBusy(true);
    try {
      await api(base + "/environments/" + env.name + "/deployments", "POST", {
        version_id: id,
        expected_revision: env.deployment_revision,
        idempotency_key: crypto.randomUUID(),
      });
      reload();
      notify(t("发布已提交，正在等待实际生效。"));
    } catch (e) {
      notify(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="grid gap-5">
      <p className="text-sm text-muted-foreground">
        {t("保存版本不会改变线上流量；部署后才会生效。")}
      </p>
      <div className="grid gap-2">
        {Object.values(data.versions)
          .filter((v) => v.environment === env.name)
          .map((v) => (
            <div
              key={v.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded-md border p-3"
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <code title={v.id}>{shortID(v.id)}</code>
                  {v.tag ? (
                    <span
                      title={v.tag}
                      className="max-w-48 truncate rounded-md bg-primary/10 px-2 py-0.5 text-xs text-primary"
                    >
                      {v.tag}
                    </span>
                  ) : null}
                </div>
                {v.message ? (
                  <p className="mt-1 break-words text-xs text-muted-foreground">{v.message}</p>
                ) : null}
              </div>
              <span className="text-xs">{shortID(v.artifact_id)}</span>
              <Button
                size="sm"
                disabled={busy || !env.enabled}
                onClick={() => void deploy(v.id)}
              >
                {t("部署此版本")}
              </Button>
            </div>
          ))}
      </div>
      <h3 className="font-medium">{t("部署记录与固定地址")}</h3>
      {deployments
        .filter((d) => d.environment === env.name)
        .map((d) => (
          <div
            key={d.id}
            className="grid gap-1 rounded-md border p-3 text-sm"
          >
            <code>
              {shortID(d.id)} · {shortID(d.version_id)} · {d.state}
            </code>
            {d.hosts.map((host) => (
              <a
                className="break-all underline"
                key={host}
                href={"https://" + host}
                target="_blank"
                rel="noreferrer"
              >
                {host}
              </a>
            ))}
            <span className="text-xs text-muted-foreground">
              {t("保留至")} {new Date(d.expires_at).toLocaleString()}
            </span>
          </div>
        ))}
    </div>
  );
}
