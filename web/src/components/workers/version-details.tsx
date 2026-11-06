import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import type { Environment, Operation, Version, WorkerData } from "@/lib/workers";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { VersionConfiguration } from "./version-configuration";
import { Status } from "./shared";

export function VersionDetails({
  base,
  id,
  data,
  env,
  operations,
  close,
}: {
  base: string;
  id: string;
  data: WorkerData;
  env: Environment;
  operations: Operation[];
  close: () => void;
}) {
  const [version, setVersion] = useState<Version | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    setVersion(null);
    setError("");
    if (!id) return;
    const controller = new AbortController();
    void api<Version>(
      base + "/versions/" + encodeURIComponent(id),
      "GET",
      undefined,
      controller.signal,
    )
      .then((result) => {
        if (!controller.signal.aborted) setVersion(result);
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [base, id]);
  return (
    <Dialog
      open={!!id}
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("版本详情")}</DialogTitle>
          <DialogDescription>
            {t("查看保存者、保存时间、来源构建与此版本冻结的配置。")}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {error}
          </p>
        ) : !version ? (
          <p role="status">{t("正在加载版本…")}</p>
        ) : (
          <div className="grid min-w-0 gap-4 text-sm">
            <dl className="grid gap-3 break-words">
              <div>
                <dt className="text-muted-foreground">{t("版本 ID")}</dt>
                <dd className="break-all font-mono">{version.id}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t("环境")}</dt>
                <dd>{version.environment}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t("保存者")}</dt>
                <dd>
                  {version.created_by_name || version.created_by || t("未记录")}
                  {version.created_by_name && version.created_by ? (
                    <span className="ml-2 break-all text-xs text-muted-foreground">
                      {version.created_by}
                    </span>
                  ) : null}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t("创建时间")}</dt>
                <dd>
                  <time dateTime={version.created_at}>
                    {new Date(version.created_at).toLocaleString()}
                  </time>
                </dd>
              </div>
              {version.tag ? (
                <div>
                  <dt className="text-muted-foreground">{t("版本标签")}</dt>
                  <dd>{version.tag}</dd>
                </div>
              ) : null}
              {version.message ? (
                <div>
                  <dt className="text-muted-foreground">{t("版本说明")}</dt>
                  <dd className="whitespace-pre-wrap">{version.message}</dd>
                </div>
              ) : null}
              <div>
                <dt className="text-muted-foreground">{t("制品")}</dt>
                <dd className="break-all font-mono">{version.artifact_id}</dd>
              </div>
              {version.archive_sha256 ? (
                <div>
                  <dt className="text-muted-foreground">SHA-256</dt>
                  <dd className="break-all font-mono">{version.archive_sha256}</dd>
                </div>
              ) : null}
            </dl>
            {env.confirmed_version_id === version.id ? (
              <p className="font-medium text-primary">{t("当前生效版本")}</p>
            ) : null}
            {env.version_id === version.id && env.confirmed_version_id !== version.id ? (
              <p>{t("正在生效的版本")}</p>
            ) : null}
            <div className="grid gap-2 border-t pt-3">
              <h4 className="font-medium">{t("关联构建")}</h4>
              {data.builds
                .filter((build) => build.artifact_id === version.artifact_id)
                .map((build) => (
                  <div
                    key={build.id}
                    className="break-all"
                  >
                    <code>{build.id}</code>
                    {build.repository_source ? (
                      <p className="mt-1 text-xs text-muted-foreground">
                        {build.repository_source.repository} · {build.repository_source.ref} ·{" "}
                        {build.repository_source.commit}
                      </p>
                    ) : null}
                  </div>
                ))}
              {!data.builds.some((build) => build.artifact_id === version.artifact_id) ? (
                <p className="text-muted-foreground">{t("无关联构建记录")}</p>
              ) : null}
            </div>
            <div className="grid gap-2 border-t pt-3">
              <h4 className="font-medium">
                {t("查看冻结配置")} · r{version.configuration_revision}
              </h4>
              <VersionConfiguration config={version} />
            </div>
            {operations
              .filter((item) => item.version_id === version.id)
              .map((item) => (
                <div
                  key={item.id}
                  className="flex flex-wrap items-center gap-2 border-t pt-3"
                >
                  <code className="break-all">{item.id}</code>
                  <Status state={item.state} />
                </div>
              ))}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
