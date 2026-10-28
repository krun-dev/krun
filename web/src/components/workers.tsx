import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  GitBranch,
  Plus,
  RefreshCw,
} from "lucide-react";
import { api, type List, type Resource } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import {
  pending,
  shortID,
  useWorkerData,
  type Build,
  type WorkerServiceSummary,
  type Operation,
  type RepositoryBinding,
  type WorkerDomain,
  type WorkerNetworkOption,
} from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Table,
  TableHeader,
  TableHead,
  TableRow,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import {
  Empty,
  Field,
  Loading,
  PageHeader,
  SelectField,
} from "@/components/common";
import { cn } from "@/lib/utils";
import { RepositoryBuild, RepositorySettings } from "@/components/workers-repository";

import {
  Status,
  artifactColors,
  artifactIcons,
  idempotency,
  workerURL,
} from "./workers/shared";
import { OperationPanel, ReleaseSummary } from "./workers/release";
import { ServiceDomains } from "./workers/domains";
import { EnvironmentSettings, Invocation } from "./workers/environment";

export function WorkersPage({
  base,
  system,
  notify,
}: {
  base: string;
  system: boolean;
  notify: (message: string) => void;
}) {
  useTranslation();
  const [services, setServices] = useState<WorkerServiceSummary[] | null>(null);
  const [service, setService] = useState(
    () => new URLSearchParams(location.search).get("worker") || "",
  );
  const [error, setError] = useState("");
  const [create, setCreate] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const [domains, setDomains] = useState<WorkerDomain[]>([]);
  const [networkOptions, setNetworkOptions] = useState<WorkerNetworkOption[]>([]);
  useEffect(() => {
    if (system) return;
    const c = new AbortController();
    // The catalog is platform configuration; it rarely changes but the labels
    // and available suffixes drive the environment picker.
    void Promise.all([
      api<List<WorkerDomain>>(base + "/workers/domains", "GET", undefined, c.signal),
      api<List<WorkerNetworkOption>>(
        base + "/workers/network-options",
        "GET",
        undefined,
        c.signal,
      ),
    ])
      .then(([domainResult, optionResult]) => {
        setDomains(domainResult.items);
        setNetworkOptions(optionResult.items);
      })
      .catch(() => {
        setDomains([]);
        setNetworkOptions([]);
      });
    return () => c.abort();
  }, [base, system]);
  useEffect(() => {
    if (system) return;
    const c = new AbortController();
    void api<List<WorkerServiceSummary>>(
      base + "/workers/services",
      "GET",
      undefined,
      c.signal,
    )
      .then((r) => {
        setServices(r.items);
        setService((v) => (r.items.some((s) => s.id === v) ? v : ""));
        setError("");
      })
      .catch((e) => {
        if (!c.signal.aborted) setError(errorMessage(e));
      });
    return () => c.abort();
  }, [base, system, revision]);
  useEffect(() => {
    const onPopState = () => {
      const id = new URLSearchParams(location.search).get("worker") || "";
      setService(id);
      if (!id) setRevision((v) => v + 1);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  function openService(id: string) {
    const url = new URL(location.href);
    url.searchParams.set("page", "workers");
    url.searchParams.set("workspace", base.split("/").at(-1) || "");
    if (id) url.searchParams.set("worker", id);
    else url.searchParams.delete("worker");
    url.searchParams.delete("environment");
    history.pushState(null, "", url);
    setService(id);
    if (!id) setRevision((v) => v + 1);
  }
  const selected = services?.find((s) => s.id === service);
  if (system)
    return (
      <Empty
        title={t("请切换到组织空间")}
        description={t("选择组织后，创建 Worker 并构建发布。")}
      />
    );
  return (
    <>
      <PageHeader
        eyebrow="WORKERS"
        title={t("Workers")}
        description={t("构建代码，选择环境发布，并查看实际运行结果。")}
        action={
          <Button onClick={() => setCreate(true)}>
            <Plus size={16} />
            {t("创建 Worker")}
          </Button>
        }
      />
      {error ? (
        <p role="alert" className="mb-4 text-sm text-destructive">
          {error}
        </p>
      ) : null}
      {services === null ? (
        <Loading />
      ) : services.length === 0 ? (
        <Empty
          title={t("还没有 Worker")}
          description={t("创建一个服务，关联代码仓库后开始构建。")}
        />
      ) : selected ? (
        <>
          <Button
            variant="ghost"
            className="mb-5 -ml-3"
            onClick={() => openService("")}
          >
            <ArrowLeft size={16} />
            {t("返回服务列表")}
          </Button>
          <WorkerDetail
            key={selected.id}
            base={base + "/services/" + selected.id + "/workers"}
            organizationBase={base}
            service={selected}
            domains={domains}
            networkOptions={networkOptions}
            notify={notify}
          />
        </>
      ) : (
        <Card className="py-0 shadow-none">
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("服务名称")}</TableHead>
                  <TableHead>{t("代码仓库")}</TableHead>
                  <TableHead>{t("环境状态")}</TableHead>
                  <TableHead className="w-28">{t("操作")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {services.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>
                      <button
                        className="text-sm font-medium hover:underline"
                        onClick={() => openService(item.id)}
                      >
                        {item.name}
                      </button>
                      <p className="mt-1 font-mono text-xs text-muted-foreground">
                        {item.id}
                      </p>
                    </TableCell>
                    <TableCell>
                      {item.repository_source ? (
                        <div className="text-sm">
                          <span className="inline-flex items-center gap-1">
                            <GitBranch size={14} />
                            {item.repository_source.repository}
                          </span>
                          <p className="text-xs text-muted-foreground">
                            {item.repository_source.ref} ·{" "}
                            {item.repository_source.root_directory}
                          </p>
                        </div>
                      ) : (
                        <span className="text-sm text-muted-foreground">
                          {t("未关联仓库")}
                        </span>
                      )}
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-2">
                        {item.environments.length ? (
                          item.environments.map((env) => (
                            <Badge key={env.name} variant="outline">
                              {env.name} ·{" "}
                              {t(
                                !env.enabled
                                  ? "未启用"
                                  : env.release_id
                                    ? env.release_id ===
                                      env.confirmed_release_id
                                      ? "已发布"
                                      : "生效中"
                                    : "未发布",
                              )}
                            </Badge>
                          ))
                        ) : (
                          <span className="text-xs text-muted-foreground">
                            {t("尚未构建")}
                          </span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => openService(item.id)}
                      >
                        {t("查看服务")}
                        <ArrowRight size={14} />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
      <Dialog open={create} onOpenChange={setCreate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("创建 Worker")}</DialogTitle>
            <DialogDescription>
              {t("Worker 归属于当前组织，环境和版本分别管理。")}
            </DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-4"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              try {
                const r = await api<Resource>(base + "/services", "POST", {
                  name,
                });
                openService(r.id);
                setRevision((v) => v + 1);
                setCreate(false);
                setName("");
                notify(t("Worker 已创建。"));
              } catch (e) {
                notify(errorMessage(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <Field label={t("Worker 名称")}>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                maxLength={128}
              />
            </Field>
            <Button type="submit" disabled={busy}>
              {t("创建 Worker")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

function WorkerDetail({
  base,
  organizationBase,
  service,
  domains,
  networkOptions,
  notify,
}: {
  base: string;
  organizationBase: string;
  service: Resource;
  domains: WorkerDomain[];
  networkOptions: WorkerNetworkOption[];
  notify: (s: string) => void;
}) {
  const [revision, setRevision] = useState(0);
  const [binding, setBinding] = useState<RepositoryBinding | null>(null);
  const reload = useCallback(() => setRevision((v) => v + 1), []);
  useEffect(() => {
    const controller = new AbortController();
    void api<RepositoryBinding>(base + "/repository", "GET", undefined, controller.signal)
      .then(setBinding)
      .catch(() => {});
    return () => controller.abort();
  }, [base, revision]);
  const { data, error } = useWorkerData(base, revision);
  const [environment, setEnvironment] = useState(
    new URLSearchParams(location.search).get("environment") || "production",
  );
  const [tab, setTab] = useState("delivery");
  const [editor, setEditor] = useState(false);
  const [publish, setPublish] = useState<Build | null>(null);
  const [busy, setBusy] = useState(false);
  const [selectedOperation, setSelectedOperation] = useState("");
  const [acceptedOperation, setAcceptedOperation] = useState<Operation | null>(
    null,
  );
  const [buildInfo, setBuildInfo] = useState<{
    build: Build;
    log: string;
    artifact?: unknown;
  } | null>(null);
  const [rollback, setRollback] = useState<{
    id: string;
    revision: number;
  } | null>(null);
  const [previewDialog, setPreviewDialog] = useState(false);
  const [previewName, setPreviewName] = useState("");
  const [previewDelete, setPreviewDelete] = useState<string | null>(null);
  const env =
    data?.environments.find((e) => e.name === environment) ||
    data?.environments[0];
  const operations =
    acceptedOperation &&
    !data?.operations.some((o) => o.id === acceptedOperation.id)
      ? [acceptedOperation, ...(data?.operations || [])]
      : data?.operations || [];
  const operation =
    operations.find(
      (o) => o.id === selectedOperation && o.environment === env?.name,
    ) || operations.find((o) => o.environment === env?.name);
  const rollbackBusy = operations.some(
    (o) =>
      o.environment === env?.name &&
      pending(o.state) &&
      o.state !== "committed",
  );
  const active = operations.some(
    (o) => o.environment === env?.name && pending(o.state),
  );
  async function createPreview() {
    setBusy(true);
    try {
      await api(base + "/environments", "POST", { name: previewName });
      setPreviewName("");
      setPreviewDialog(false);
      reload();
    } catch (e) {
      notify(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function deletePreview() {
    if (!previewDelete) return;
    setBusy(true);
    try {
      await api(
        base + "/environments/" + encodeURIComponent(previewDelete),
        "DELETE",
      );
      if (environment === previewDelete) setEnvironment("production");
      setPreviewDelete(null);
      reload();
    } catch (e) {
      notify(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function submitRelease(build: Build) {
    if (!env) return;
    setBusy(true);
    try {
      const op = await api<Operation>(
        base + "/environments/" + env.name + "/releases",
        "POST",
        {
          build_id: build.id,
          expected_revision: env.release_revision,
          idempotency_key: idempotency(),
        },
      );
      setAcceptedOperation(op);
      setSelectedOperation(op.id);
      setPublish(null);
      reload();
      notify(t("发布已提交，正在等待实际生效。"));
    } catch (e) {
      notify(errorMessage(e));
      reload();
    } finally {
      setBusy(false);
    }
  }
  async function inspectBuild(b: Build) {
    try {
      const [logs, artifact] = await Promise.all([
        api<{ log: string }>(base + "/builds/" + b.id + "/logs"),
        b.artifact_id
          ? api(base + "/artifacts/" + b.artifact_id)
          : Promise.resolve(undefined),
      ]);
      setBuildInfo({ build: b, log: logs.log, artifact });
    } catch (e) {
      notify(errorMessage(e));
    }
  }
  if (!data)
    return error ? (
      <p role="alert" className="text-destructive">
        {error}
        <Button variant="outline" onClick={reload} className="ml-3">
          {t("重试")}
        </Button>
      </p>
    ) : (
      <Loading />
    );
  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-medium">{service.name}</h2>
          <code className="text-xs text-muted-foreground">{service.id}</code>
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="icon"
            aria-label={t("刷新状态")}
            onClick={reload}
          >
            <RefreshCw size={16} />
          </Button>
          <Button onClick={() => setEditor(true)}>{t("构建新版本")}</Button>
        </div>
      </div>
      {error ? (
        <p
          role="alert"
          className="rounded-md border border-destructive/20 p-3 text-sm text-destructive"
        >
          {error} · {t("保留上次数据，正在重新连接。")}
        </p>
      ) : null}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <span>{t("基线环境常驻对外服务；预览环境用于验证，可随时删除。")}</span>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={busy || data.environments.length - 1 >= 32}
          onClick={() => setPreviewDialog(true)}
        >
          {t("创建预览环境")}
        </Button>
      </div>
      <div className="grid gap-3 md:grid-cols-3">
        {data.environments.map((e) => (
          <button
            key={e.name}
            onClick={() => setEnvironment(e.name)}
            aria-pressed={env?.name === e.name}
            className={cn(
              "rounded-lg border bg-card p-4 text-left",
              env?.name === e.name
                ? "border-primary ring-1 ring-primary/15"
                : "hover:border-primary/40",
            )}
          >
            <div className="flex items-center justify-between">
              <strong>{e.name}</strong>
              <Badge
                variant="outline"
                className={cn(
                  e.enabled &&
                    e.release_id === e.confirmed_release_id &&
                    e.release_id
                    ? "border-primary/40 text-primary"
                    : e.enabled && e.release_id !== e.confirmed_release_id
                      ? "border-amber-500/40 text-amber-600 dark:text-amber-500"
                      : "",
                )}
              >
                {!e.enabled
                  ? t("未启用")
                  : e.release_id !== e.confirmed_release_id
                    ? t("生效中")
                    : e.release_id
                      ? t("已发布")
                      : t("未发布")}
              </Badge>
            </div>
            <div className="my-3 text-sm">
              {t("Release")} r{e.release_revision}{" "}
              <span className="text-muted-foreground">
                · {shortID(e.release_id)}
              </span>
            </div>
            <div className="grid gap-1">
              {(e.hosts || []).map((host, i) => {
                const d = domains.find((x) => x.id === (e.domains || [])[i]);
                const text = d?.label ? `${host}（${d.label}）` : host;
                return e.release_id && e.enabled ? (
                  <a
                    key={host}
                    href={workerURL(host)}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 break-all text-[11px] text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground"
                    onClick={(event) => event.stopPropagation()}
                  >
                    {text}
                    <ArrowUpRight size={11} className="shrink-0" />
                  </a>
                ) : (
                  <code
                    key={host}
                    className="break-all text-[11px] text-muted-foreground"
                  >
                    {text}
                  </code>
                );
              })}
              {(e.hosts || []).length === 0 ? (
                <code className="break-all text-[11px] text-muted-foreground">
                  {t("未绑定域名")}
                </code>
              ) : null}
            </div>
            {e.name !== "production" ? (
              <div className="mt-2 flex justify-end">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={(event) => {
                    event.stopPropagation();
                    setPreviewDelete(e.name);
                  }}
                >
                  {t("删除")}
                </Button>
              </div>
            ) : null}
          </button>
        ))}
      </div>
      {env ? (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="delivery">{t("构建与发布")}</TabsTrigger>
            <TabsTrigger value="source">{t("代码源")}</TabsTrigger>
            <TabsTrigger value="routing">{t("路由与域名")}</TabsTrigger>
            <TabsTrigger value="settings">{t("环境配置")}</TabsTrigger>
            <TabsTrigger value="invoke">{t("调用测试")}</TabsTrigger>
          </TabsList>
          <TabsContent value="delivery" className="mt-5 grid gap-5">
            {!binding?.source ? (
              <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-primary/25 bg-primary/5 px-4 py-3 text-sm">
                <p className="text-muted-foreground">
                  {t("此服务尚未绑定代码仓库；绑定后可直接从分支或 Tag 构建版本。")}
                </p>
                <Button variant="outline" size="sm" onClick={() => setTab("source")}>
                  {t("绑定代码仓库")}
                </Button>
              </div>
            ) : null}
            <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
              {Object.entries(artifactIcons).map(([label, Icon]) => (
                <span
                  key={label}
                  className={cn(
                    "inline-flex items-center gap-1.5",
                    artifactColors[label],
                  )}
                >
                  <Icon size={14} />
                  {t(label)}
                </span>
              ))}
            </div>
            <Card className="py-0 shadow-none">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("构建记录")}</TableHead>
                    <TableHead>{t("状态")}</TableHead>
                    <TableHead className="min-w-48">{t("已部署到")}</TableHead>
                    <TableHead>{t("操作")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.builds.map((b) => (
                    <TableRow key={b.id}>
                      <TableCell>
                        <code className="text-xs">{shortID(b.id)}</code>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {b.entrypoint} ·{" "}
                          {new Date(b.created_at).toLocaleString()}
                        </p>
                        {b.repository_source ? (
                          <p className="mt-1 text-xs text-muted-foreground">
                            {b.repository_source.repository} ·{" "}
                            {b.repository_source.ref} ·{" "}
                            {b.repository_source.commit?.slice(0, 12) ||
                              t("等待解析 Commit")}
                          </p>
                        ) : null}
                      </TableCell>
                      <TableCell>
                        <Status state={b.state} />
                        {b.error ? (
                          <p className="mt-1 max-w-xs text-xs text-destructive">
                            {b.error}
                          </p>
                        ) : null}
                      </TableCell>
                      <TableCell>
                        <ReleaseSummary
                          build={b}
                          data={data}
                          onSelect={(name) => setEnvironment(name)}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => void inspectBuild(b)}
                          >
                            {t("日志与制品")}
                          </Button>
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={
                              b.state !== "succeeded" || active || !env.enabled
                            }
                            onClick={() => setPublish(b)}
                          >
                            {t("发布")}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                  {data.builds.length === 0 ? (
                    <TableRow>
                      <TableCell
                        colSpan={4}
                        className="py-10 text-center text-muted-foreground"
                      >
                        {t("还没有构建，点击构建新版本。")}
                      </TableCell>
                    </TableRow>
                  ) : null}
                </TableBody>
              </Table>
            </Card>
            {operation ? (
              <OperationPanel
                operation={operation}
                operations={operations.filter(
                  (o) => o.environment === env.name,
                )}
                select={setSelectedOperation}
              />
            ) : null}
            <Card className="py-0 shadow-none">
              <CardContent className="p-5">
                <h3 className="mb-4 font-medium">
                  {env.name} · {t("发布历史")}
                </h3>
                {(data.histories[env.name] || []).length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    {t("尚无成功生效的发布。")}
                  </p>
                ) : (
                  (data.histories[env.name] || []).map((h) => (
                    <div
                      key={h.operation_id}
                      className="flex flex-wrap items-center justify-between gap-3 border-t py-3 first:border-0"
                    >
                      <div>
                        <span className="font-medium">r{h.revision}</span>{" "}
                        <code className="ml-2 text-xs">
                          {shortID(h.release_id)}
                        </code>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {h.kind} · {new Date(h.time).toLocaleString()}
                        </p>
                      </div>
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={
                          rollbackBusy ||
                          !env.enabled ||
                          env.release_id === h.release_id
                        }
                        onClick={() =>
                          setRollback({
                            id: h.release_id,
                            revision: h.revision,
                          })
                        }
                      >
                        {t("回滚到此版本")}
                      </Button>
                    </div>
                  ))
                )}
              </CardContent>
            </Card>
          </TabsContent>
          <TabsContent value="source" className="mt-5 grid gap-3">
            <h3 className="flex items-center gap-2 text-sm font-medium">
              {t("代码仓库")}
            </h3>
            <RepositorySettings
              base={base}
              organizationBase={organizationBase}
              bound={binding?.source ?? null}
              revision={binding?.revision ?? 0}
              onSaved={setBinding}
              notify={notify}
            />
          </TabsContent>
          <TabsContent value="routing" className="mt-5 grid gap-5">
            <ServiceDomains
              base={base}
              domains={domains}
                networkOptions={networkOptions}
              environments={data.environments}
              revision={data.domainsRevision}
              active={active}
              reload={reload}
              notify={notify}
            />
          </TabsContent>
          <TabsContent value="settings" className="mt-5 grid gap-6">
            <EnvironmentSettings
              key={env.name}
              base={base}
              env={env}
              active={active}
              data={data}
              domains={domains}
              reload={reload}
              notify={notify}
            />
          </TabsContent>
          <TabsContent value="invoke" className="mt-5">
            <Invocation key={env.name} base={base} env={env} />
          </TabsContent>
        </Tabs>
      ) : null}
      <Dialog open={previewDialog} onOpenChange={setPreviewDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("创建预览环境")}</DialogTitle>
            <DialogDescription>
              {t("预览环境是临时的验证场所，验证通过后可将同一制品晋升到基线。")}
            </DialogDescription>
          </DialogHeader>
          <Field label={t("预览名称")} hint={t("小写字母、数字、连字符，例如 fix-auth。")}>
            <Input
              aria-label={t("预览名称")}
              value={previewName}
              onChange={(e) => setPreviewName(e.target.value)}
              placeholder="fix-auth"
              required
              maxLength={64}
              disabled={busy}
            />
          </Field>
          <Button
            disabled={busy || !/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(previewName) || previewName === "production"}
            onClick={() => void createPreview()}
          >
            {busy ? t("创建中…") : t("创建")}
          </Button>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!previewDelete}
        onOpenChange={(v) => {
          if (!v && !busy) setPreviewDelete(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("删除预览环境")}</DialogTitle>
            <DialogDescription>
              {t("删除后路由立即失效，历史构建记录保留。此操作不可撤销。")}
            </DialogDescription>
          </DialogHeader>
          <p className="text-sm font-medium">{previewDelete}</p>
          <Button
            variant="destructive"
            disabled={busy}
            onClick={() => void deletePreview()}
          >
            {busy ? t("删除中…") : t("确认删除")}
          </Button>
        </DialogContent>
      </Dialog>
      <RepositoryBuild
        open={editor}
        close={() => setEditor(false)}
        base={base}
        bound={binding?.source ?? null}
        repositoryRevision={binding?.revision ?? 0}
        done={() => {
          reload();
          setEditor(false);
        }}
        notify={notify}
      />
      <Dialog
        open={!!publish}
        onOpenChange={(v) => {
          if (!v && !busy) setPublish(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("发布构建版本")}</DialogTitle>
            <DialogDescription>
              {t("选用成功制品与目标环境当前配置，候选验证通过后提交发布。")}
            </DialogDescription>
          </DialogHeader>
          <Field label={t("目标环境")}>
            <SelectField value={env?.name || ""} onChange={setEnvironment}>
              {data.environments
                .filter((e) => e.enabled)
                .map((e) => (
                  <option key={e.name} value={e.name}>
                    {e.name}
                  </option>
                ))}
            </SelectField>
          </Field>
          <p className="text-sm">
            {shortID(publish?.id)} → {env?.name} / r{env?.release_revision}
          </p>
          <pre className="max-h-40 overflow-auto rounded-md bg-secondary p-3 text-xs">
            {JSON.stringify(env?.variables, null, 2)}
          </pre>
          <Button
            disabled={busy || active || !env?.enabled}
            onClick={() => {
              if (publish) void submitRelease(publish);
            }}
          >
            {busy ? t("提交中…") : t("确认发布")}
          </Button>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!rollback}
        onOpenChange={(v) => {
          if (!v && !busy) setRollback(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("回滚历史版本")}</DialogTitle>
            <DialogDescription>
              {t("恢复该历史 Release 的代码和变量，仍需验证并等待生效。")}
            </DialogDescription>
          </DialogHeader>
          <p className="text-sm">
            {env?.name} → r{rollback?.revision} · {shortID(rollback?.id)}
          </p>
          <Button
            disabled={busy}
            onClick={async () => {
              if (!env || !rollback) return;
              setBusy(true);
              try {
                const op = await api<Operation>(
                  base + "/environments/" + env.name + "/rollbacks",
                  "POST",
                  {
                    release_id: rollback.id,
                    expected_revision: env.release_revision,
                    idempotency_key: idempotency(),
                  },
                );
                setAcceptedOperation(op);
                setSelectedOperation(op.id);
                setRollback(null);
                reload();
                notify(t("回滚已提交。"));
              } catch (e) {
                notify(errorMessage(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            {t("确认回滚")}
          </Button>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!buildInfo}
        onOpenChange={(v) => {
          if (!v) setBuildInfo(null);
        }}
      >
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("日志与制品")}</DialogTitle>
            <DialogDescription>{buildInfo?.build.id}</DialogDescription>
          </DialogHeader>
          {buildInfo?.build.repository_source ? (
            <pre className="rounded-md border p-3 text-xs whitespace-pre-wrap">
              {JSON.stringify(buildInfo.build.repository_source, null, 2)}
            </pre>
          ) : null}
          <pre className="max-h-56 overflow-auto whitespace-pre-wrap rounded-md bg-secondary p-4 text-xs">
            {buildInfo?.log || t("构建尚未完成，日志将在完成后保存。")}
          </pre>
          {buildInfo?.artifact ? (
            <pre className="max-h-56 overflow-auto whitespace-pre-wrap rounded-md border p-4 text-xs">
              {JSON.stringify(buildInfo.artifact, null, 2)}
            </pre>
          ) : null}
        </DialogContent>
      </Dialog>
    </div>
  );
}

// Per-build release chips: only environments this build reached appear,
// colored by the shared release state system.
