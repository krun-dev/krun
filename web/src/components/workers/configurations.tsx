import { useEffect, useRef, useState } from "react";
import { Copy, Plus, RefreshCw, Rocket } from "lucide-react";
import { api, type List } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import { shortID, type Environment, type Operation, type WorkerData } from "@/lib/workers";
import {
  confirmedConfigurationVersion,
  type WorkerConfiguration,
} from "@/lib/worker-configurations";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { SelectField } from "@/components/common";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { ConfigurationEditor, type ConfigurationSource } from "./configuration-editor";
import { ConfigurationDiff } from "./configuration-diff";
import { OperationPanel } from "./version";

type Confirmation = {
  configuration: WorkerConfiguration;
  revision: number;
  configurationRevision: number;
  code: string;
  build: string;
  key: string;
};

export function ConfigurationsPanel({
  base,
  organizationBase,
  env,
  data,
  active,
  reload,
  notify,
  operations,
  operation,
  deployed,
  selectOperation,
}: {
  base: string;
  organizationBase: string;
  env: Environment;
  data: WorkerData;
  active: boolean;
  reload: () => void;
  notify: (message: string) => void;
  operations: Operation[];
  operation?: Operation;
  deployed: (operation: Operation) => void;
  selectOperation: (id: string) => void;
}) {
  const [configurations, setConfigurations] = useState<WorkerConfiguration[]>([]);
  const [selectedId, setSelectedId] = useState(
    () => new URLSearchParams(location.search).get("configuration") || "",
  );
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [source, setSource] = useState<ConfigurationSource | null>(null);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const [busy, setBusy] = useState(false);
  const [deployError, setDeployError] = useState("");
  const submitting = useRef(false);
  const current = confirmedConfigurationVersion(env, data.versions);
  const selected = configurations.find((c) => c.id === selectedId) || configurations[0];
  const currentConfiguration = configurations.find((c) => c.id === current?.configuration_id);
  const builds = data.builds.filter((build) => build.state === "succeeded");
  const stale =
    !!confirmation &&
    (confirmation.revision !== env.deployment_revision ||
      confirmation.configurationRevision !== env.configuration_revision ||
      confirmation.code !== (env.version_id || ""));
  const ready =
    env.enabled && !active && (!env.version_id || env.version_id === env.confirmed_version_id);
  const histories = new Set(
    (data.histories[env.name] || []).map(
      (history) => data.versions[history.version_id]?.configuration_id,
    ),
  );

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setLoadError("");
    void api<List<WorkerConfiguration>>(
      base + "/environments/" + env.name + "/configurations",
      "GET",
      undefined,
      controller.signal,
    )
      .then((result) => {
        if (!controller.signal.aborted) setConfigurations(result.items);
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setLoadError(errorMessage(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [base, env.name, refresh]);

  function select(configuration: WorkerConfiguration) {
    setSelectedId(configuration.id);
    const url = new URL(location.href);
    url.searchParams.set("configuration", configuration.id);
    history.replaceState(null, "", url);
  }

  function confirmPublish() {
    if (!selected) return;
    setDeployError("");
    setConfirmation({
      configuration: selected,
      revision: env.deployment_revision,
      configurationRevision: env.configuration_revision,
      code: env.version_id || "",
      build: builds[0]?.id || "",
      key: crypto.randomUUID(),
    });
  }

  async function publish() {
    if (!confirmation || stale || submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setDeployError("");
    try {
      const result = await api<Operation>(
        base +
          "/environments/" +
          env.name +
          "/configurations/" +
          confirmation.configuration.id +
          "/deployments",
        "POST",
        {
          ...(confirmation.code
            ? { code_version_id: confirmation.code }
            : { build_id: confirmation.build }),
          expected_revision: confirmation.revision,
          expected_configuration_revision: confirmation.configurationRevision,
          idempotency_key: confirmation.key,
        },
      );
      setConfirmation(null);
      deployed(result);
      reload();
      notify(t("配置版本已进入发布队列，等待运行时确认后生效。"));
    } catch (cause) {
      setDeployError(errorMessage(cause));
      reload();
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <div className="grid gap-5">
      <Card className="py-0 shadow-none">
        <CardContent className="grid gap-2 p-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline">{current ? t("当前生效") : t("尚未部署")}</Badge>
              <strong className="break-all text-sm font-medium">
                {current
                  ? currentConfiguration?.name || current.tag || shortID(current.id)
                  : t("配置保存后需发布才会运行。")}
              </strong>
            </div>
            <code className="text-xs text-muted-foreground">{shortID(current?.id)}</code>
          </div>
          {current ? (
            <p className="break-all text-xs text-muted-foreground">
              {t("运行代码制品")} · {shortID(current.artifact_id)}
            </p>
          ) : null}
          {active || env.version_id !== env.confirmed_version_id ? (
            <p className="text-xs text-muted-foreground">
              {t("发布正在进行，当前生效标记将在运行时确认后更新。")}
            </p>
          ) : null}
        </CardContent>
      </Card>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="font-medium">
            {env.name} · {t("配置版本")}
          </h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("配置可命名、复制和恢复；未生效历史保留 30 天，当前及发布中的配置继续保留。")}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setRefresh((value) => value + 1)}
            disabled={loading}
            aria-label={t("刷新配置历史")}
          >
            <RefreshCw size={14} />
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setSource({})}
          >
            <Plus size={14} />
            {t("从环境草稿创建")}
          </Button>
          {current ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setSource({ version: current })}
            >
              {t("保存当前生效配置")}
            </Button>
          ) : null}
        </div>
      </div>
      {loadError ? (
        <div
          role="alert"
          className="text-sm text-destructive"
        >
          {loadError}
          <Button
            variant="ghost"
            onClick={() => setRefresh((value) => value + 1)}
          >
            {t("重试")}
          </Button>
        </div>
      ) : loading ? (
        <p
          role="status"
          className="text-sm text-muted-foreground"
        >
          {t("加载中…")}
        </p>
      ) : configurations.length === 0 ? (
        <p className="rounded-md border p-5 text-sm text-muted-foreground">
          {t("尚无独立配置版本。可以从环境草稿或当前生效配置保存第一份。")}
        </p>
      ) : (
        <div className="grid min-w-0 gap-5 lg:grid-cols-[minmax(220px,1fr)_minmax(0,2fr)]">
          <div
            className="grid content-start gap-2"
            aria-label={t("配置历史")}
          >
            {configurations.map((c) => (
              <button
                key={c.id}
                type="button"
                aria-pressed={selected?.id === c.id}
                onClick={() => select(c)}
                className={cn(
                  "grid w-full min-w-0 gap-2 rounded-md border p-4 text-left",
                  selected?.id === c.id ? "border-primary bg-primary/5" : "hover:bg-secondary/50",
                )}
              >
                <span className="flex flex-wrap items-center justify-between gap-2">
                  <strong className="break-all text-sm font-medium">{c.name}</strong>
                  <Badge variant="outline">
                    {current?.configuration_id === c.id
                      ? t("当前生效")
                      : histories.has(c.id)
                        ? t("历史")
                        : t("已保存")}
                  </Badge>
                </span>
                <span className="break-all text-xs text-muted-foreground">
                  {shortID(c.id)} · {new Date(c.created_at).toLocaleString()}
                </span>
              </button>
            ))}
          </div>
          {selected ? (
            <section
              className="grid min-w-0 content-start gap-4 rounded-md border p-5"
              aria-label={t("配置版本详情")}
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h3 className="break-all font-medium">{selected.name}</h3>
                <Badge variant="outline">{t("内容固定")}</Badge>
              </div>
              {selected.message ? (
                <p className="whitespace-pre-wrap break-all text-sm text-muted-foreground">
                  {selected.message}
                </p>
              ) : null}
              <ConfigurationDiff
                selected={selected}
                current={current}
              />
              <div className="flex flex-wrap justify-end gap-2">
                <Button
                  variant="outline"
                  onClick={() => setSource({ configuration: selected })}
                >
                  <Copy size={14} />
                  {t("复制并修改")}
                </Button>
                <Button
                  onClick={confirmPublish}
                  disabled={
                    !ready ||
                    current?.configuration_id === selected.id ||
                    (!current && builds.length === 0)
                  }
                >
                  <Rocket size={14} />
                  {current?.configuration_id === selected.id
                    ? t("当前已生效")
                    : histories.has(selected.id)
                      ? t("恢复并发布")
                      : t("发布并生效")}
                </Button>
              </div>
              {!current && builds.length === 0 ? (
                <p className="text-xs text-muted-foreground">
                  {t("请先完成一次代码构建，再将此配置用于首次部署。")}
                </p>
              ) : null}
            </section>
          ) : null}
        </div>
      )}
      {operation ? (
        <OperationPanel
          operation={operation}
          operations={operations.filter((item) => item.environment === env.name)}
          select={selectOperation}
        />
      ) : null}
      {source ? (
        <ConfigurationEditor
          base={base}
          organizationBase={organizationBase}
          env={env}
          source={source}
          close={() => setSource(null)}
          saved={(configuration) => {
            setConfigurations((previous) => [
              configuration,
              ...previous.filter((item) => item.id !== configuration.id),
            ]);
            select(configuration);
            setSource(null);
            notify(t("配置版本已保存，当前运行未改变。"));
            setRefresh((value) => value + 1);
          }}
        />
      ) : null}
      <Dialog
        open={!!confirmation}
        onOpenChange={(open) => {
          if (!open && !busy) setConfirmation(null);
        }}
      >
        <DialogContent
          className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl"
          showCloseButton={!busy}
        >
          <DialogHeader>
            <DialogTitle>
              {env.name === "production" ? t("确认发布到生产环境") : t("确认发布配置版本")}
            </DialogTitle>
            <DialogDescription>
              {t(
                "使用所选配置生成新的发布版本。复用当前代码，部署成功后切换；旧配置与旧发布记录保留。",
              )}
            </DialogDescription>
          </DialogHeader>
          {confirmation ? (
            <>
              <p className="break-all text-sm">
                {confirmation.configuration.name} → <strong>{env.name}</strong>
              </p>
              {confirmation.code ? (
                <p className="text-sm">
                  {t("复用代码版本")} · <code>{shortID(confirmation.code)}</code>
                </p>
              ) : (
                <SelectField
                  label={t("首次部署使用的构建")}
                  value={confirmation.build}
                  onChange={(build) =>
                    setConfirmation({ ...confirmation, build, key: crypto.randomUUID() })
                  }
                  disabled={busy}
                >
                  {builds.map((build) => (
                    <option
                      key={build.id}
                      value={build.id}
                    >
                      {shortID(build.id)} · {build.entrypoint}
                    </option>
                  ))}
                </SelectField>
              )}
              <ConfigurationDiff
                selected={confirmation.configuration}
                current={current}
              />
            </>
          ) : null}
          {stale ? (
            <p
              role="alert"
              className="text-sm text-destructive"
            >
              {t("部署或环境草稿已改变，请关闭确认框后重新核对。")}
            </p>
          ) : null}
          {deployError ? (
            <p
              role="alert"
              className="text-sm text-destructive"
            >
              {deployError}
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
              disabled={busy || stale || active || (!confirmation?.code && !confirmation?.build)}
              onClick={() => void publish()}
            >
              {busy ? t("发布中…") : t("确认发布")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
