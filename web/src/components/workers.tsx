import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ArrowLeft,
  ArrowRight,
  GitBranch,
  Check,
  Circle,
  Clock,
  History as HistoryIcon,
  LoaderCircle,
  Minus,
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
  type Environment,
  type Operation,
  type WorkerData,
  type RepositoryBinding,
} from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
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

const states: Record<string, string> = {
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
const artifactIcons: Record<string, typeof Check> = {
  当前: Check,
  历史: HistoryIcon,
  未发布: Circle,
  生效中: Clock,
  不可用: Minus,
};
const artifactColors: Record<string, string> = {
  当前: "text-emerald-700 dark:text-emerald-400",
  历史: "text-sky-700 dark:text-sky-400",
  未发布: "text-muted-foreground",
  生效中: "text-amber-700 dark:text-amber-400",
  不可用: "text-muted-foreground",
};
const stepStates: Record<string, string> = {
  pending: "等待执行",
  running: "处理中",
  done: "已完成",
  failed: "失败",
};
const stepNames: Record<string, string> = {
  artifact: "制品检查",
  candidate: "候选验证",
  commit: "提交 Release",
  synchronize: "运行副本确认",
};
function Status({ state }: { state: string }) {
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
const idempotency = () => crypto.randomUUID();

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
                                  : env.deployment_id
                                    ? env.deployment_id ===
                                      env.confirmed_deployment_id
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
  notify,
}: {
  base: string;
  organizationBase: string;
  service: Resource;
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
              <Badge variant="outline">
                {!e.enabled
                  ? t("未启用")
                  : e.deployment_id !== e.confirmed_deployment_id
                    ? t("生效中")
                    : e.deployment_id
                      ? t("已发布")
                      : t("未发布")}
              </Badge>
            </div>
            <div className="my-3 text-sm">
              {t("Release")} r{e.release_revision}{" "}
              <span className="text-muted-foreground">
                · {shortID(e.deployment_id)}
              </span>
            </div>
            <div className="flex items-center justify-between gap-2">
              <code className="break-all text-[11px] text-muted-foreground">
                {e.host}
              </code>
              {e.name !== "production" ? (
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
              ) : null}
            </div>
          </button>
        ))}
      </div>
      {env ? (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="delivery">{t("构建与发布")}</TabsTrigger>
            <TabsTrigger value="settings">{t("环境配置")}</TabsTrigger>
            <TabsTrigger value="invoke">{t("调用测试")}</TabsTrigger>
          </TabsList>
          <TabsContent value="delivery" className="mt-5 grid gap-5">
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
                    {data.environments
                      .filter((e) => e.enabled)
                      .map((e) => (
                        <TableHead key={e.name}>{e.name}</TableHead>
                      ))}
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
                      {data.environments
                        .filter((e) => e.enabled)
                        .map((e) => (
                          <TableCell key={e.name}>
                            <ArtifactState build={b} env={e} data={data} />
                          </TableCell>
                        ))}
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
                        colSpan={5}
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
                          {shortID(h.deployment_id)}
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
                          env.deployment_id === h.deployment_id
                        }
                        onClick={() =>
                          setRollback({
                            id: h.deployment_id,
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
          <TabsContent value="settings" className="mt-5 grid gap-6">
            <section className="grid gap-3">
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
            </section>
            <EnvironmentSettings
              key={env.name}
              base={base}
              env={env}
              active={active}
              data={data}
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
        environments={data.environments}
        selectedEnvironment={env?.name || "production"}
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
              {t("恢复该历史 Deployment 的代码和变量，仍需验证并等待生效。")}
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
                    deployment_id: rollback.id,
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

function ArtifactState({
  build,
  env,
  data,
}: {
  build: Build;
  env: Environment;
  data: WorkerData;
}) {
  const current = env.deployment_id
    ? data.deployments[env.deployment_id]
    : undefined;
  const historical = (data.histories[env.name] || []).some(
    (h) => data.deployments[h.deployment_id]?.artifact_id === build.artifact_id,
  );
  const unavailable = build.state !== "succeeded";
  const state = unavailable
    ? "不可用"
    : current?.artifact_id === build.artifact_id
      ? env.deployment_id !== env.confirmed_deployment_id
        ? "生效中"
        : "当前"
      : historical
        ? "历史"
        : "未发布";
  const Icon = artifactIcons[state];
  return (
    <span
      title={t(state)}
      className={cn(
        "inline-flex items-center gap-1.5 text-xs",
        artifactColors[state],
      )}
    >
      <Icon size={14} />
      {t(state)}
    </span>
  );
}

function OperationPanel({
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

function EnvironmentSettings({
  base,
  env,
  active,
  data,
  reload,
  notify,
}: {
  base: string;
  env: Environment;
  active: boolean;
  data: WorkerData;
  reload: () => void;
  notify: (s: string) => void;
}) {
  const [variables, setVariables] = useState(
    JSON.stringify(env.variables, null, 2),
  );
  const [revision, setRevision] = useState(env.configuration_revision);
  const [busy, setBusy] = useState(false);
  const current = env.deployment_id
    ? data.deployments[env.deployment_id]
    : undefined;
  const stale = revision !== env.configuration_revision;
  async function save(input: unknown) {
    setBusy(true);
    try {
      const result = await api<Environment>(
        base + "/environments/" + env.name,
        "PATCH",
        input,
      );
      setRevision(result.configuration_revision);
      reload();
      notify(t("环境配置已保存，下次发布时生效。"));
    } catch (e) {
      notify(errorMessage(e));
      reload();
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card className="py-0 shadow-none">
      <CardContent className="grid gap-5 p-5">
        <div className="flex items-center justify-between">
          <h3 className="font-medium">
            {env.name} · {t("环境配置")}
          </h3>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={env.enabled}
              disabled={busy || active}
              onChange={(e) =>
                void save({
                  expected_revision: env.configuration_revision,
                  enabled: e.target.checked,
                })
              }
            />
            {t("启用环境")}
          </label>
        </div>
        <Field
          label={t("下一次发布使用的变量")}
          hint={t(
            "普通变量以 JSON 对象填写，保存配置不会修改正在运行的版本。请勿填入 Secret。",
          )}
        >
          <Textarea
            aria-label={t("环境变量 JSON")}
            className="min-h-40 font-mono text-xs"
            value={variables}
            onChange={(e) => setVariables(e.target.value)}
          />
        </Field>
        {stale ? (
          <p role="alert" className="text-sm text-destructive">
            {t("配置已被更新，请先加载最新配置。")}
          </p>
        ) : null}
        <div className="flex gap-2">
          <Button
            disabled={busy || active || stale}
            onClick={() => {
              try {
                const parsed: unknown = JSON.parse(variables);
                if (
                  !parsed ||
                  typeof parsed !== "object" ||
                  Array.isArray(parsed) ||
                  Object.values(parsed).some((v) => typeof v !== "string")
                )
                  throw new Error(t("变量必须是字符串键值对象。"));
                void save({ expected_revision: revision, variables: parsed });
              } catch (e) {
                notify(errorMessage(e));
              }
            }}
          >
            {t("保存配置")}
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              setVariables(JSON.stringify(env.variables, null, 2));
              setRevision(env.configuration_revision);
            }}
          >
            {t("加载最新配置")}
          </Button>
        </div>
        <div className="border-t pt-4">
          <p className="mb-2 text-sm text-muted-foreground">
            {t("当前 Release 使用的变量")} · {shortID(current?.id)}
          </p>
          <pre className="rounded-md bg-secondary p-3 text-xs">
            {JSON.stringify(current?.variables || {}, null, 2)}
          </pre>
        </div>
      </CardContent>
    </Card>
  );
}

function Invocation({ base, env }: { base: string; env: Environment }) {
  const [path, setPath] = useState("/");
  const [method, setMethod] = useState("GET");
  const [body, setBody] = useState("{}");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<{
    status: number;
    body: string;
    headers: Record<string, string>;
    truncated: boolean;
  } | null>(null);
  let display = result?.body || "";
  try {
    display = JSON.stringify(JSON.parse(display), null, 2);
  } catch {
    /* Non-JSON responses stay escaped text. */
  }
  return (
    <Card className="py-0 shadow-none">
      <CardContent className="grid gap-5 p-5">
        <div>
          <h3 className="font-medium">
            {env.name} · {t("调用已发布 Worker")}
          </h3>
          <p className="mt-1 break-all text-xs text-muted-foreground">
            {env.host}
          </p>
        </div>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError("");
            try {
              setResult(
                await api(
                  base + "/environments/" + env.name + "/invoke",
                  "POST",
                  {
                    path,
                    method,
                    body: method === "GET" || method === "HEAD" ? "" : body,
                  },
                ),
              );
            } catch (e) {
              setError(errorMessage(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="flex gap-3">
            <div className="w-28">
              <SelectField
                label={t("请求方法")}
                value={method}
                onChange={setMethod}
              >
                {[
                  "GET",
                  "POST",
                  "PUT",
                  "PATCH",
                  "DELETE",
                  "HEAD",
                  "OPTIONS",
                ].map((m) => (
                  <option key={m}>{m}</option>
                ))}
              </SelectField>
            </div>
            <Input
              aria-label={t("请求路径")}
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/orders?id=1"
              required
            />
          </div>
          {method !== "GET" && method !== "HEAD" ? (
            <Field label={t("请求体")}>
              <Textarea
                value={body}
                onChange={(e) => setBody(e.target.value)}
                className="font-mono text-xs"
              />
            </Field>
          ) : null}
          <Button
            className="w-fit"
            disabled={busy || !env.enabled || !env.deployment_id}
            type="submit"
          >
            {busy ? t("调用中…") : t("发送请求")}
          </Button>
        </form>
        {!env.deployment_id ? (
          <p className="text-sm text-muted-foreground">
            {t("先发布到此环境，再发送请求。")}
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {result ? (
          <div className="grid gap-3">
            <Badge variant="outline" className="w-fit">
              HTTP {result.status}
            </Badge>
            <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded-md bg-secondary p-4 text-xs">
              {display}
            </pre>
            <details className="text-xs text-muted-foreground">
              <summary>{t("响应信息")}</summary>
              <pre className="mt-2 whitespace-pre-wrap break-all">
                {JSON.stringify(result.headers, null, 2)}
              </pre>
            </details>
            {result.truncated ? <p>{t("响应超过 64 KiB，已截断。")}</p> : null}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
