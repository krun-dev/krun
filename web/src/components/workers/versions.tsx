import { useEffect, useRef, useState } from "react";
import { Check, LoaderCircle, Rocket } from "lucide-react";
import { api, APIError } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import {
  pending,
  shortID,
  type WorkerData,
  type Environment,
  type Operation,
  type Version,
} from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { OperationPanel } from "./version";
import { Status } from "./shared";
import { useCollectionPage } from "@/lib/collection-page";
import { CollectionPagination, CollectionToolbar } from "@/components/collection-controls";
import { VersionDetails } from "./version-details";
import { VersionConfiguration } from "./version-configuration";

type Deployment = {
  id: string;
  version_id: string;
  candidate_version?: string;
  candidate_weight?: number;
  message?: string;
  environment: string;
  state: string;
  hosts: string[];
  expires_at: string;
  created_at: string;
  committed_at?: string;
  operation_id: string;
};

function versionState(
  version: Version,
  env: Environment,
  data: WorkerData,
  operations: Operation[],
) {
  if (version.id === env.candidate_version) {
    return t("灰度版本");
  }
  if (version.id === env.version_id) {
    return version.id === env.confirmed_version_id ? t("当前部署") : t("生效中");
  }
  if (version.id === env.confirmed_version_id) {
    return t("上次已确认");
  }
  const operation = operations.find(
    (item) => item.version_id === version.id && item.environment === env.name,
  );
  if (operation && pending(operation.state)) {
    return t("部署中");
  }
  if ((data.histories[env.name] || []).some((entry) => entry.version_id === version.id)) {
    return t("历史部署");
  }
  if (operation?.state === "failed") {
    return t("部署失败");
  }
  if (operation?.state === "superseded") {
    return t("已被替换");
  }
  return t("待部署");
}

export function VersionsPanel({
  base,
  env,
  data,
  reload,
  notify,
  createdVersion,
  operations,
  operation,
  selectOperation,
  deployed,
}: {
  base: string;
  env: Environment;
  data: WorkerData;
  reload: () => void;
  notify: (s: string) => void;
  createdVersion: Version | null;
  operations: Operation[];
  operation?: Operation;
  selectOperation: (id: string) => void;
  deployed: (operation: Operation) => void;
}) {
  const [selectedVersion, setSelectedVersion] = useState("");
  const progress = useRef<HTMLDivElement>(null);
  const focusedVersion = useRef("");
  const [busy, setBusy] = useState(false);
  const [percentage, setPercentage] = useState("100");
  const [confirmation, setConfirmation] = useState<{
    version: Version;
    revision: number;
  } | null>(null);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const submitting = useRef(false);
  const deploymentKey = useRef("");
  const createdCard = useRef<HTMLDivElement>(null);
  const active = operations.some((item) => item.environment === env.name && pending(item.state));
  const refreshKey = JSON.stringify([
    Object.keys(data.versions).sort(),
    createdVersion?.id,
    env.deployment_revision,
    env.confirmed_version_id,
    operations.map((item) => [item.id, item.state]),
  ]);
  const versionPage = useCollectionPage<Version>(
    base + "/versions?environment=" + encodeURIComponent(env.name),
    true,
    refreshKey,
  );
  const deploymentPage = useCollectionPage<Deployment>(
    base + "/deployments?environment=" + encodeURIComponent(env.name),
    true,
    refreshKey,
  );
  const versions = [...(versionPage.items || [])];
  if (
    createdVersion?.environment === env.name &&
    versionPage.pageNumber === 1 &&
    !versionPage.search &&
    !versions.some((version) => version.id === createdVersion.id)
  ) {
    versions.unshift(createdVersion);
  }
  const environmentVersions = versions.slice(0, versionPage.pageSize);
  const weight = Math.round(Number(percentage) * 1000);
  const validPercentage =
    /^\d{1,3}(?:\.\d{1,3})?$/.test(percentage) &&
    weight >= 0 &&
    weight <= 100000 &&
    (weight === 0 || weight === 100000 || (weight >= 10 && weight <= 99990));
  const canSplit = !!env.version_id && confirmation?.version.id !== env.version_id;

  useEffect(() => {
    if (!createdVersion || focusedVersion.current === createdVersion.id) return;
    const frame = requestAnimationFrame(() => {
      if (createdCard.current) {
        createdCard.current.focus({ preventScroll: true });
        createdCard.current.scrollIntoView({ block: "nearest" });
        focusedVersion.current = createdVersion.id;
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [createdVersion?.id, versionPage.items]);

  async function deploy() {
    if (!confirmation || submitting.current || stale || !validPercentage) {
      return;
    }
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const result = await api<Operation>(
        base +
          "/environments/" +
          encodeURIComponent(confirmation.version.environment) +
          "/deployments",
        "POST",
        {
          ...(canSplit && weight !== 100000
            ? {
                versions: [
                  { version_id: env.version_id, percentage: (100000 - weight) / 1000 },
                  { version_id: confirmation.version.id, percentage: weight / 1000 },
                ],
              }
            : { version_id: confirmation.version.id }),
          expected_revision: confirmation.revision,
          idempotency_key: deploymentKey.current,
        },
      );
      deployed(result);
      requestAnimationFrame(() => {
        progress.current?.scrollIntoView({
          block: "nearest",
          behavior: "smooth",
        });
      });
      setConfirmation(null);
      reload();
      notify(t("发布已提交，正在等待实际生效。"));
    } catch (cause) {
      if (cause instanceof APIError && cause.code === "REVISION_CONFLICT") {
        setError(t("当前部署已改变，请关闭后重新核对再部署。"));
        setStale(true);
      } else {
        setError(errorMessage(cause));
      }
      reload();
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <div className="grid gap-5">
      <section
        aria-label={t("当前部署")}
        className="grid gap-3 rounded-md border bg-muted/30 p-4"
      >
        <h3 className="font-medium">
          {env.name} · {t("当前部署")}
        </h3>
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span>{t("当前生效版本")}</span>
          {env.confirmed_version_id ? (
            <Button
              variant="link"
              size="sm"
              className="h-auto min-w-0 shrink whitespace-normal break-all p-0 text-left font-mono"
              onClick={() => setSelectedVersion(env.confirmed_version_id!)}
            >
              {env.confirmed_version_id}
            </Button>
          ) : (
            <span className="text-muted-foreground">{t("尚无已确认生效的版本")}</span>
          )}
          {!env.enabled ? <Badge variant="outline">{t("已禁用")}</Badge> : null}
        </div>
        {env.candidate_version ? (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span>
              {t("灰度版本")} · {(env.candidate_weight ?? 0) / 1000}%
            </span>
            <Button
              variant="link"
              size="sm"
              className="h-auto min-w-0 shrink whitespace-normal break-all p-0 text-left font-mono"
              onClick={() => setSelectedVersion(env.candidate_version!)}
            >
              {env.candidate_version}
            </Button>
          </div>
        ) : null}
        {env.version_id && env.version_id !== env.confirmed_version_id ? (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <LoaderCircle
              size={14}
              className="animate-spin"
            />
            <span>{t("正在生效的版本")}</span>
            <Button
              variant="link"
              size="sm"
              className="h-auto min-w-0 shrink whitespace-normal break-all p-0 text-left font-mono"
              onClick={() => setSelectedVersion(env.version_id!)}
            >
              {env.version_id}
            </Button>
          </div>
        ) : null}
      </section>
      <div
        ref={progress}
        className="scroll-mt-6"
      >
        {operation ? (
          <OperationPanel
            operation={operation}
            operations={operations.filter((item) => item.environment === env.name)}
            select={selectOperation}
          />
        ) : null}
      </div>
      <section
        aria-label={t("版本列表")}
        className="grid gap-3"
      >
        <div>
          <h3 className="font-medium">
            {env.name} · {t("版本列表")}
          </h3>
          <p className="mt-2 text-sm text-muted-foreground">
            {t(
              "版本固定代码与创建时的环境配置。待部署版本不会改变线上流量；选择“部署此版本”后才会启动发布。",
            )}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("需要提前准备版本时，在“构建”的更多操作中选择“创建待部署版本”。")}
          </p>
        </div>
        <CollectionToolbar
          page={versionPage}
          label={t("搜索版本 ID、标签、说明或保存者")}
        />
        {versionPage.error ? (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {versionPage.error}
          </p>
        ) : null}
        {versionPage.loading ? (
          <p
            role="status"
            className="text-sm text-muted-foreground"
          >
            {t("正在加载版本…")}
          </p>
        ) : null}
        <div className="grid gap-3">
          {environmentVersions.length === 0 && !versionPage.loading && !versionPage.error ? (
            <p className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">
              {versionPage.search || versionPage.hasPrevious
                ? t("没有匹配的版本。")
                : t("此环境还没有版本。可以直接发布成功构建，或先创建待部署版本。")}
            </p>
          ) : (
            environmentVersions.map((version) => {
              const justCreated = version.id === createdVersion?.id;
              const awaitingDeployment =
                justCreated &&
                version.id !== env.version_id &&
                !operations.some((item) => item.version_id === version.id);
              const sourceBuilds = data.builds.filter(
                (build) => build.artifact_id === version.artifact_id,
              );
              const state = versionState(version, env, data, operations);
              return (
                <div
                  key={version.id}
                  ref={justCreated ? createdCard : undefined}
                  tabIndex={-1}
                  aria-label={t("版本 {{id}}", { id: version.id })}
                  className={cn(
                    "grid scroll-mt-6 gap-3 rounded-md border p-4 outline-none focus-visible:ring-2 focus-visible:ring-primary",
                    justCreated && "border-primary/40 bg-primary/5",
                  )}
                >
                  {awaitingDeployment ? (
                    <p
                      role="status"
                      className="flex items-center gap-2 text-sm text-primary"
                    >
                      <Check size={16} />
                      {t("待部署版本已创建，线上部署未改变。")}
                    </p>
                  ) : null}
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <Button
                          variant="link"
                          size="sm"
                          className="h-auto p-0 font-mono"
                          title={version.id}
                          onClick={() => setSelectedVersion(version.id)}
                        >
                          {shortID(version.id)}
                        </Button>
                        {version.tag ? (
                          <Badge
                            title={version.tag}
                            variant="secondary"
                            className="max-w-64 truncate"
                          >
                            {version.tag}
                          </Badge>
                        ) : null}
                        <Badge variant="outline">{state}</Badge>
                      </div>
                      <p className="mt-2 break-words text-xs text-muted-foreground">
                        {t("保存者")} ·{" "}
                        <span title={version.created_by}>
                          {version.created_by_name || version.created_by || t("未记录")}
                        </span>
                      </p>
                      <p className="mt-2 text-xs text-muted-foreground">
                        {t("创建时间")} ·{" "}
                        <time dateTime={version.created_at}>
                          {new Date(version.created_at).toLocaleString()}
                        </time>
                      </p>
                      {version.message ? (
                        <p className="mt-2 whitespace-pre-wrap break-words text-sm">
                          {version.message}
                        </p>
                      ) : null}
                    </div>
                    <Button
                      size="sm"
                      disabled={
                        busy ||
                        active ||
                        !env.enabled ||
                        (version.id === env.version_id && !env.candidate_version)
                      }
                      onClick={() => {
                        deploymentKey.current = crypto.randomUUID();
                        setError("");
                        setStale(false);
                        setPercentage(
                          version.id === env.candidate_version
                            ? String((env.candidate_weight ?? 0) / 1000)
                            : "100",
                        );
                        setConfirmation({
                          version,
                          revision: env.deployment_revision,
                        });
                      }}
                    >
                      <Rocket size={14} />
                      {t("部署此版本")}
                    </Button>
                  </div>
                  <dl className="grid gap-2 text-xs text-muted-foreground sm:grid-cols-2">
                    <div>
                      <dt>{t("关联构建")}</dt>
                      <dd className="mt-1 grid gap-2 break-all">
                        {sourceBuilds.length
                          ? sourceBuilds.map((build) => (
                              <div key={build.id}>
                                <code title={build.id}>{shortID(build.id)}</code>
                                {build.repository_source ? (
                                  <p className="mt-1">
                                    {build.repository_source.repository} ·{" "}
                                    {build.repository_source.ref} ·{" "}
                                    {build.repository_source.commit?.slice(0, 12)}
                                  </p>
                                ) : null}
                              </div>
                            ))
                          : t("无关联构建记录")}
                      </dd>
                    </div>
                  </dl>
                  <details className="border-t pt-3">
                    <summary className="cursor-pointer text-sm">{t("查看冻结配置")}</summary>
                    <div className="mt-3">
                      <VersionConfiguration config={version} />
                    </div>
                  </details>
                </div>
              );
            })
          )}
        </div>
        <CollectionPagination page={versionPage} />
      </section>
      <section
        aria-label={t("部署记录与固定地址")}
        className="grid gap-3"
      >
        <h3 className="font-medium">{t("部署记录与固定地址")}</h3>
        <CollectionToolbar
          page={deploymentPage}
          label={t("搜索部署 ID、版本 ID 或状态")}
        />
        {deploymentPage.error ? (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {deploymentPage.error}
          </p>
        ) : null}
        {deploymentPage.loading ? (
          <p
            role="status"
            className="text-sm text-muted-foreground"
          >
            {t("正在加载部署记录…")}
          </p>
        ) : null}
        {deploymentPage.items?.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {deploymentPage.search || deploymentPage.hasPrevious
              ? t("没有匹配的部署记录。")
              : t("尚无部署记录。固定地址在部署验证通过并提交后生成。")}
          </p>
        ) : null}
        {deploymentPage.items?.map((deployment) => (
          <div
            key={deployment.id}
            className="grid gap-2 rounded-md border p-3 text-sm"
          >
            <div className="flex flex-wrap items-center gap-2">
              <code title={deployment.id}>{shortID(deployment.id)}</code>
              <Status state={deployment.state} />
              {deployment.version_id === env.confirmed_version_id &&
              deployment.state === "available" ? (
                <Badge>{t("当前生效版本")}</Badge>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <span>{t("版本")}</span>
              <Button
                variant="link"
                size="sm"
                className="h-auto p-0 font-mono"
                title={deployment.version_id}
                onClick={() => setSelectedVersion(deployment.version_id)}
              >
                {shortID(deployment.version_id)}
              </Button>
            </div>
            {deployment.candidate_version ? (
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span>
                  {t("灰度版本")} · {(deployment.candidate_weight ?? 0) / 1000}%
                </span>
                <Button
                  variant="link"
                  size="sm"
                  className="h-auto p-0 font-mono"
                  onClick={() => setSelectedVersion(deployment.candidate_version!)}
                >
                  {shortID(deployment.candidate_version)}
                </Button>
              </div>
            ) : null}
            {deployment.message ? <p className="break-words">{deployment.message}</p> : null}
            <span className="text-xs text-muted-foreground">
              {t("部署时间")} ·{" "}
              <time dateTime={deployment.created_at}>
                {new Date(deployment.created_at).toLocaleString()}
              </time>
            </span>
            {deployment.committed_at ? (
              <span className="text-xs text-muted-foreground">
                {t("提交时间")} ·{" "}
                <time dateTime={deployment.committed_at}>
                  {new Date(deployment.committed_at).toLocaleString()}
                </time>
              </span>
            ) : null}
            {deployment.hosts.map((host) => (
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
              {t("保留至")} {new Date(deployment.expires_at).toLocaleString()}
            </span>
            {operations.some((item) => item.id === deployment.operation_id) ? (
              <Button
                variant="outline"
                size="sm"
                className="justify-self-start"
                onClick={() => {
                  selectOperation(deployment.operation_id);
                  requestAnimationFrame(() =>
                    progress.current?.scrollIntoView({
                      block: "nearest",
                      behavior: "smooth",
                    }),
                  );
                }}
              >
                {t("查看发布进度")}
              </Button>
            ) : null}
          </div>
        ))}
        <CollectionPagination page={deploymentPage} />
      </section>
      <VersionDetails
        base={base}
        id={selectedVersion}
        data={data}
        env={env}
        operations={operations}
        close={() => setSelectedVersion("")}
      />
      <Dialog
        open={!!confirmation}
        onOpenChange={(open) => {
          if (!open && !busy) setConfirmation(null);
        }}
      >
        <DialogContent
          className="max-h-[85dvh] overflow-y-auto"
          showCloseButton={!busy}
        >
          <DialogHeader>
            <DialogTitle>{t("部署已保存版本")}</DialogTitle>
            <DialogDescription>
              {t(
                "将使用此版本创建时冻结的代码和配置，替换目标环境的当前部署。之后对环境配置的修改不会合入此版本。",
              )}
            </DialogDescription>
          </DialogHeader>
          {confirmation ? (
            <>
              <p className="break-all text-sm">
                <code>{shortID(confirmation.version.id)}</code> → {confirmation.version.environment}
              </p>
              {confirmation.version.tag ? (
                <p className="break-all text-sm font-medium">{confirmation.version.tag}</p>
              ) : null}
              {canSplit ? (
                <label className="grid gap-2 text-sm">
                  {t("此版本流量百分比")}
                  <input
                    className="w-32 rounded-md border bg-background px-3 py-2"
                    type="number"
                    min="0"
                    max="100"
                    step="0.001"
                    value={percentage}
                    onChange={(event) => setPercentage(event.target.value)}
                  />
                </label>
              ) : null}
              <VersionConfiguration config={confirmation.version} />
            </>
          ) : null}
          {error ? (
            <p
              role="alert"
              className="text-sm text-destructive"
            >
              {error}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setConfirmation(null)}
            >
              {t("取消")}
            </Button>
            <Button
              disabled={busy || active || stale || !env.enabled || !validPercentage}
              onClick={() => void deploy()}
            >
              {busy ? (
                <LoaderCircle
                  size={14}
                  className="animate-spin"
                />
              ) : (
                <Rocket size={14} />
              )}
              {busy ? t("提交中…") : t("确认部署")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
