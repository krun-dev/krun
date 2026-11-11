import { useCollectionPage } from "@/lib/collection-page";
import { CollectionToolbar, CollectionPagination } from "@/components/collection-controls";
import { readBuildLog } from "../lib/build-logs";
import { BuildInspector, type BuildArtifact } from "./workers/build-inspector";
import { LocalUpload } from "./workers/local-upload";
import { ConfigurationsPanel } from "./workers/configurations";
import { PreviewBasePanel } from "./workers/preview-base";
import { VersionsPanel } from "./workers/versions";
import { BuildVersionMenu, CreateVersionDialog } from "./workers/version-create";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  GitBranch,
  FileText,
  LoaderCircle,
  Rocket,
  Plus,
  RefreshCw,
} from "lucide-react";
import { api, APIError, type List, type Resource } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import { useSuggestedShortName } from "@/lib/suggested-short-name";
import {
  pending,
  shortID,
  useWorkerData,
  type Build,
  type WorkerSummary,
  type Operation,
  type RepositoryBinding,
  type WorkerDomain,
  type WorkerNetworkProfile,
  type WorkerRouting,
  type Environment,
  type Version,
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
import { Empty, Field, Loading, PageHeader, SelectField } from "@/components/common";
import { cn } from "@/lib/utils";
import { RepositoryBuild, RepositorySettings } from "@/components/workers-repository";

import { Status, artifactColors, artifactIcons, idempotency, workerURL } from "./workers/shared";
import { OperationPanel, VersionSummary } from "./workers/version";
import { WorkerDomains } from "./workers/domains";
import { EnvironmentSettings, Invocation } from "./workers/environment";

function initialNetworkProfile(profiles: WorkerNetworkProfile[]) {
  return (
    profiles.find((profile) => profile.default && !profile.approval_required)?.id ||
    profiles.find((profile) => !profile.approval_required)?.id ||
    ""
  );
}

export function WorkersPage({
  base,
  detail,
  canCreate,
  notify,
}: {
  base: string;
  detail?: { id: string; back: () => void };
  canCreate: boolean;
  notify: (message: string) => void;
}) {
  useTranslation();
  const [worker, setWorker] = useState(
    () => detail?.id || new URLSearchParams(location.search).get("worker") || "",
  );
  const [detailError, setDetailError] = useState("");
  const [selected, setSelected] = useState<WorkerSummary | null>(null);
  const [create, setCreate] = useState(false);
  const [createError, setCreateError] = useState("");
  const [createdWorker, setCreatedWorker] = useState<{
    id: string;
    reference: string;
    revision: number;
  } | null>(null);
  const createRequest = useRef<AbortController | null>(null);
  const [name, setName] = useState("");
  const locator = useSuggestedShortName(name, 63);
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const collection = useCollectionPage<WorkerSummary>(base + "/workers", !worker, revision);
  const workers = collection.items;
  const error = worker ? detailError : collection.error;
  const [networkProfiles, setNetworkProfiles] = useState<WorkerNetworkProfile[]>([]);
  const [networkProfile, setNetworkProfile] = useState("");
  const selectedProfile = networkProfiles.find(
    (profile) => profile.id === networkProfile && !profile.approval_required,
  );
  useEffect(() => () => createRequest.current?.abort(), []);
  useEffect(() => {
    if (!canCreate) {
      return;
    }
    const c = new AbortController();
    void api<List<WorkerNetworkProfile>>(
      base + "/workers/network-profiles",
      "GET",
      undefined,
      c.signal,
    )
      .then((result) => {
        setNetworkProfiles(result.items);
        setNetworkProfile((current) =>
          result.items.some((profile) => profile.id === current && !profile.approval_required)
            ? current
            : initialNetworkProfile(result.items),
        );
      })
      .catch(() => {
        setNetworkProfiles([]);
      });
    return () => c.abort();
  }, [base, canCreate]);
  useEffect(() => {
    setSelected(null);
    setDetailError("");
    if (!worker) {
      return;
    }
    const controller = new AbortController();
    void api<List<WorkerSummary>>(
      base + "/workers?worker=" + encodeURIComponent(worker),
      "GET",
      undefined,
      controller.signal,
    )
      .then((result) => {
        if (!controller.signal.aborted) {
          setSelected(result.items[0] || null);
          if (!result.items[0]) {
            setDetailError(t("Worker不存在或无权访问"));
          }
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setDetailError(errorMessage(cause));
        }
      });
    return () => controller.abort();
  }, [base, worker, revision]);
  useEffect(() => {
    if (detail) return;
    const onPopState = () => {
      const id = new URLSearchParams(location.search).get("worker") || "";
      setWorker(id);
      if (!id) setRevision((v) => v + 1);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [detail]);
  function openWorker(id: string) {
    if (detail && !id) {
      detail.back();
      return;
    }
    const url = new URL(location.href);
    url.searchParams.set("page", "workers");
    url.searchParams.set("workspace", base.split("/").at(-1) || "");
    if (id) url.searchParams.set("worker", id);
    else url.searchParams.delete("worker");
    url.searchParams.delete("environment");
    url.searchParams.delete("worker_tab");
    history.pushState(null, "", url);
    setWorker(id);
    if (!id) setRevision((v) => v + 1);
  }

  function resetCreatedWorker() {
    setCreatedWorker(null);
    setName("");
    locator.reset();
    setRevision((value) => value + 1);
  }

  function openCreatedWorker(reference: string) {
    setCreate(false);
    setCreateError("");
    resetCreatedWorker();
    openWorker(reference);
  }

  async function createWorker() {
    if (createRequest.current || !selectedProfile || (!canCreate && !createdWorker)) {
      return;
    }

    const controller = new AbortController();
    createRequest.current = controller;
    setBusy(true);
    setCreateError("");
    try {
      let target = createdWorker;
      if (!target) {
        const resource = await api<Resource>(
          base + "/workers",
          "POST",
          { name, locator: locator.value },
          controller.signal,
        );
        if (controller.signal.aborted) {
          return;
        }
        target = { id: resource.id, reference: resource.locator || resource.id, revision: 0 };
        setCreatedWorker(target);
      }

      const workerPath = base + "/workers/" + encodeURIComponent(target.id);
      try {
        await api(
          workerPath + "/network-profile",
          "PUT",
          { network_profile: selectedProfile.id, expected_revision: target.revision },
          controller.signal,
        );
      } catch (cause) {
        if (!(cause instanceof APIError) || cause.code !== "REVISION_CONFLICT") {
          throw cause;
        }

        const current = await api<WorkerRouting>(
          workerPath + "/domains",
          "GET",
          undefined,
          controller.signal,
        );
        if (controller.signal.aborted) {
          return;
        }
        // The previous PUT may have succeeded before its response was lost.
        if (current.network_profile.id !== selectedProfile.id) {
          setCreatedWorker({ ...target, revision: current.revision });
          throw cause;
        }
      }

      if (controller.signal.aborted) {
        return;
      }
      openCreatedWorker(target.reference);
      notify(t("Worker 已创建。"));
    } catch (cause) {
      if (!controller.signal.aborted) {
        setCreateError(errorMessage(cause));
      }
    } finally {
      createRequest.current = null;
      if (!controller.signal.aborted) {
        setBusy(false);
      }
    }
  }

  return (
    <>
      <PageHeader
        eyebrow="WORKERS"
        title={t("Workers")}
        description={t("构建代码，选择环境发布，并查看实际运行结果。")}
        action={
          canCreate && !detail ? (
            <Button
              onClick={() => {
                setNetworkProfile(initialNetworkProfile(networkProfiles));
                setCreate(true);
              }}
            >
              <Plus size={16} />
              {t("创建 Worker")}
            </Button>
          ) : undefined
        }
      />
      {error ? (
        <div
          role="alert"
          className="mb-4 flex items-center gap-3 text-sm text-destructive"
        >
          {error}
          <Button
            variant="outline"
            size="sm"
            onClick={() => setRevision((value) => value + 1)}
          >
            {t("重试")}
          </Button>
        </div>
      ) : null}
      {!worker ? (
        <CollectionToolbar
          page={collection}
          label={t("搜索 Worker 名称或 ID")}
        />
      ) : null}
      {worker && !selected ? (
        !detailError ? (
          <Loading />
        ) : (
          <Button
            variant="outline"
            onClick={() => openWorker("")}
          >
            {t("返回Worker列表")}
          </Button>
        )
      ) : selected ? (
        <>
          <Button
            variant="ghost"
            className="mb-5 -ml-3"
            onClick={() => openWorker("")}
          >
            <ArrowLeft size={16} />
            {t("返回Worker列表")}
          </Button>
          <WorkerDetail
            key={selected.id}
            base={base + "/workers/" + encodeURIComponent(selected.locator || selected.id)}
            organizationBase={base}
            worker={selected}
            networkProfiles={networkProfiles}
            notify={notify}
          />
        </>
      ) : collection.loading || workers === null ? (
        !error ? (
          <Loading />
        ) : null
      ) : workers.length === 0 ? (
        <Empty
          title={
            collection.search || collection.hasPrevious ? t("没有匹配结果") : t("还没有 Worker")
          }
          description={
            collection.search || collection.hasPrevious
              ? t("请调整搜索条件或返回上一页。")
              : t("创建一个Worker，关联代码仓库后开始构建。")
          }
        />
      ) : (
        <Card className="py-0 shadow-none">
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("Worker名称")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("代码仓库")}</TableHead>
                  <TableHead>{t("环境状态")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("创建时间")}</TableHead>
                  <TableHead className="hidden w-28 md:table-cell">{t("操作")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {workers.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>
                      <button
                        className="text-sm font-medium hover:underline"
                        onClick={() => openWorker(item.locator || item.id)}
                      >
                        {item.name}
                      </button>
                      <p className="mt-1 font-mono text-xs text-muted-foreground">
                        {item.locator || item.id}
                      </p>
                      <p className="mt-1 text-xs text-muted-foreground md:hidden">
                        {t("创建时间")} · {new Date(item.created_at).toLocaleString()}
                      </p>
                      {item.network_profile?.name ? (
                        <Badge
                          variant="outline"
                          className="mt-2"
                        >
                          {item.network_profile.name}
                        </Badge>
                      ) : null}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {item.repository_source ? (
                        <div className="text-sm">
                          <span className="inline-flex items-center gap-1">
                            <GitBranch size={14} />
                            {item.repository_source.repository}
                          </span>
                          <p className="text-xs text-muted-foreground">
                            {item.repository_source.ref} · {item.repository_source.root_directory}
                          </p>
                        </div>
                      ) : (
                        <span className="text-sm text-muted-foreground">{t("未关联仓库")}</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-2">
                        {item.environments.length ? (
                          item.environments.map((env) => (
                            <Badge
                              key={env.name}
                              variant="outline"
                            >
                              {env.name} ·{" "}
                              {t(
                                !env.enabled
                                  ? "未启用"
                                  : env.version_id
                                    ? env.version_id === env.confirmed_version_id
                                      ? "已发布"
                                      : "生效中"
                                    : "未发布",
                              )}
                            </Badge>
                          ))
                        ) : (
                          <span className="text-xs text-muted-foreground">{t("尚未构建")}</span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="hidden whitespace-nowrap md:table-cell">
                      {new Date(item.created_at).toLocaleString()}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => openWorker(item.locator || item.id)}
                      >
                        {t("查看Worker")}
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
      {!worker ? <CollectionPagination page={collection} /> : null}
      <Dialog
        open={create}
        onOpenChange={(open) => {
          if (open || busy) {
            return;
          }
          setCreate(false);
          setCreateError("");
          if (createdWorker) {
            resetCreatedWorker();
          }
        }}
      >
        <DialogContent showCloseButton={!busy}>
          <DialogHeader>
            <DialogTitle>{t("创建 Worker")}</DialogTitle>
            <DialogDescription>
              {t("Worker 归属于当前组织，环境和版本分别管理。")}
            </DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              void createWorker();
            }}
          >
            <Field label={t("Worker 名称")}>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                maxLength={128}
                disabled={busy || !!createdWorker}
              />
            </Field>
            <Field
              label={t("Worker 定位符（可选）")}
              hint={t("根据 Worker 名称自动填写，可修改，最多 63 个字符。可用于 Wrangler 部署。")}
            >
              <Input
                value={locator.value}
                onChange={(e) => locator.edit(e.target.value)}
                placeholder="image-resize"
                maxLength={63}
                disabled={busy || !!createdWorker}
              />
            </Field>
            {networkProfiles.length > 1 ? (
              <Field label={t("网络环境")}>
                <div className="grid gap-2 sm:grid-cols-2">
                  {networkProfiles.map((profile) => (
                    <button
                      key={profile.id}
                      type="button"
                      disabled={busy || profile.approval_required}
                      aria-pressed={networkProfile === profile.id}
                      onClick={() => setNetworkProfile(profile.id)}
                      className={cn(
                        "rounded-lg border p-3 text-left transition-colors",
                        networkProfile === profile.id
                          ? "border-primary bg-primary/5 ring-1 ring-primary/15"
                          : "hover:border-primary/40",
                        profile.approval_required && "cursor-not-allowed opacity-55",
                      )}
                    >
                      <span className="text-sm font-medium">{profile.name}</span>
                      {profile.description ? (
                        <span className="mt-1 block text-xs text-muted-foreground">
                          {profile.description}
                        </span>
                      ) : null}
                      {profile.approval_required ? (
                        <span className="mt-2 block text-xs text-muted-foreground">
                          {t("需要平台审批")}
                        </span>
                      ) : null}
                    </button>
                  ))}
                </div>
              </Field>
            ) : null}
            {createError ? (
              <div
                role="alert"
                className="grid gap-1 text-sm text-destructive"
              >
                {createdWorker ? <p>{t("Worker 已创建，网络配置尚未确认。")}</p> : null}
                <p>{createError}</p>
              </div>
            ) : null}
            <div className="grid gap-2">
              <Button
                type="submit"
                disabled={busy || !selectedProfile || (!canCreate && !createdWorker)}
              >
                {t(createdWorker ? "重试网络配置" : "创建 Worker")}
              </Button>
              {createdWorker ? (
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => openCreatedWorker(createdWorker.reference)}
                >
                  {t("查看Worker")}
                </Button>
              ) : null}
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

function WorkerDetail({
  base,
  organizationBase,
  worker,
  networkProfiles,
  notify,
}: {
  base: string;
  organizationBase: string;
  worker: WorkerSummary;
  networkProfiles: WorkerNetworkProfile[];
  notify: (s: string) => void;
}) {
  const [revision, setRevision] = useState(0);
  const [binding, setBinding] = useState<RepositoryBinding | null>(null);
  const [routing, setRouting] = useState<WorkerRouting | null>(null);
  const reload = useCallback(() => setRevision((v) => v + 1), []);
  useEffect(() => {
    const controller = new AbortController();
    void Promise.allSettled([
      api<RepositoryBinding>(base + "/repository", "GET", undefined, controller.signal),
      api<WorkerRouting>(base + "/domains", "GET", undefined, controller.signal),
    ]).then(([repository, network]) => {
      if (repository.status === "fulfilled") setBinding(repository.value);
      if (network.status === "fulfilled") setRouting(network.value);
    });
    return () => controller.abort();
  }, [base, revision]);
  const { data, error } = useWorkerData(base, revision);
  const [environment, setEnvironment] = useState(
    new URLSearchParams(location.search).get("environment") || "production",
  );
  const [tab, setTab] = useState(() => {
    const value = new URLSearchParams(location.search).get("worker_tab") || "delivery";
    const tabs = ["delivery", "versions", "source", "routing", "configurations", "invoke"];
    return tabs.includes(value) ? value : "delivery";
  });
  const [versionDraft, setVersionDraft] = useState<{
    build: Build;
    env: Environment;
  } | null>(null);
  const [createdVersion, setCreatedVersion] = useState<Version | null>(null);
  useEffect(() => {
    const url = new URL(location.href);
    url.searchParams.set("environment", environment);
    url.searchParams.set("worker_tab", tab);
    history.replaceState(null, "", url);
  }, [environment, tab]);
  const [editor, setEditor] = useState(false);
  const [publish, setPublish] = useState<Build | null>(null);
  const [busy, setBusy] = useState(false);
  const [selectedOperation, setSelectedOperation] = useState("");
  const [acceptedOperation, setAcceptedOperation] = useState<Operation | null>(null);
  const [buildInfo, setBuildInfo] = useState<{
    build: Build;
    log: string;
    pollSeconds: number | null;
    artifact?: BuildArtifact;
    artifactError?: string;
  } | null>(null);
  const [inspectingBuild, setInspectingBuild] = useState("");
  const inspectedBuildID = buildInfo?.build.id;
  const pollSeconds = buildInfo?.pollSeconds;
  useEffect(() => {
    if (!inspectedBuildID || !pollSeconds) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      try {
        const result = await readBuildLog(
          base + "/builds/" + inspectedBuildID + "/logs",
          controller.signal,
        );
        const nextPoll = result.pollSeconds;
        setBuildInfo((current) =>
          current && current.build.id === inspectedBuildID
            ? {
                ...current,
                log: result.log,
                pollSeconds: nextPoll,
                build: { ...current.build, state: result.state },
              }
            : current,
        );
        if (nextPoll) timer = setTimeout(poll, nextPoll * 1000);
      } catch (e) {
        if (!controller.signal.aborted) {
          notify(errorMessage(e));
        }
      }
    }
    timer = setTimeout(poll, pollSeconds * 1000);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [base, inspectedBuildID, pollSeconds]);
  const [rollback, setRollback] = useState<{
    id: string;
    revision: number;
  } | null>(null);
  const [previewDialog, setPreviewDialog] = useState(false);
  const [previewName, setPreviewName] = useState("");
  const [previewDelete, setPreviewDelete] = useState<string | null>(null);
  const env = data?.environments.find((e) => e.name === environment) || data?.environments[0];
  const domains: WorkerDomain[] = routing?.catalog || [];
  const operations =
    acceptedOperation && !data?.operations.some((o) => o.id === acceptedOperation.id)
      ? [acceptedOperation, ...(data?.operations || [])]
      : data?.operations || [];
  const operation =
    operations.find((o) => o.id === selectedOperation && o.environment === env?.name) ||
    operations.find((o) => o.environment === env?.name);
  const rollbackBusy = operations.some(
    (o) => o.environment === env?.name && pending(o.state) && o.state !== "committed",
  );
  const deploymentPending = (name?: string) =>
    operations.some((o) => o.environment === name && pending(o.state));
  const active = deploymentPending(env?.name);
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
      await api(base + "/environments/" + encodeURIComponent(previewDelete), "DELETE");
      if (environment === previewDelete) setEnvironment("production");
      setPreviewDelete(null);
      reload();
    } catch (e) {
      notify(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function submitVersion(build: Build) {
    if (!env) return;
    setBusy(true);
    try {
      const op = await api<Operation>(base + "/environments/" + env.name + "/deployments", "POST", {
        build_id: build.id,
        expected_revision: env.deployment_revision,
        idempotency_key: idempotency(),
      });
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
    setInspectingBuild(b.id);
    try {
      const [logs, artifactResult] = await Promise.all([
        readBuildLog(base + "/builds/" + b.id + "/logs"),
        b.artifact_id
          ? api<BuildArtifact>(base + "/artifacts/" + b.artifact_id)
              .then((artifact) => ({ artifact, error: undefined }))
              .catch((error) => ({
                artifact: undefined,
                error: errorMessage(error),
              }))
          : Promise.resolve({ artifact: undefined, error: undefined }),
      ]);
      setBuildInfo({
        build: { ...b, state: logs.state },
        log: logs.log,
        artifact: artifactResult.artifact,
        artifactError: artifactResult.error,
        pollSeconds: logs.pollSeconds,
      });
    } catch (e) {
      notify(errorMessage(e));
    } finally {
      setInspectingBuild("");
    }
  }
  if (!data)
    return error ? (
      <p
        role="alert"
        className="text-destructive"
      >
        {error}
        <Button
          variant="outline"
          onClick={reload}
          className="ml-3"
        >
          {t("重试")}
        </Button>
      </p>
    ) : (
      <Loading />
    );
  return (
    <div className="grid min-w-0 gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-xl font-medium">{worker.name}</h2>
            {routing?.network_profile.name ? (
              <Badge variant="outline">{routing.network_profile.name}</Badge>
            ) : null}
          </div>
          <code className="block break-all text-xs text-muted-foreground">
            {worker.resource_path || worker.locator || worker.id}
          </code>
          <p className="mt-2 text-xs text-muted-foreground">
            {t("创建时间")} ·{" "}
            <time dateTime={worker.created_at}>{new Date(worker.created_at).toLocaleString()}</time>
          </p>
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
          {error}
        </p>
      ) : null}
      <section
        aria-label={t("环境管理")}
        className="grid gap-3"
      >
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="grid gap-1">
            <h3 className="text-sm font-medium">{t("环境管理")}</h3>
            <p className="text-xs text-muted-foreground">
              {t("生产环境和预览环境统一管理，各自使用独立的配置与发布记录。")}
            </p>
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
                "min-w-0 rounded-lg border bg-card p-4 text-left",
                env?.name === e.name
                  ? "border-primary ring-1 ring-primary/15"
                  : "hover:border-primary/40",
              )}
            >
              <div className="flex items-center justify-between">
                <div className="min-w-0">
                  <strong className="break-all">{e.name}</strong>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {e.name === "production" ? t("生产环境") : t("预览环境")}
                  </p>
                </div>
                <Badge
                  variant="outline"
                  className={cn(
                    e.enabled && e.version_id === e.confirmed_version_id && e.version_id
                      ? "border-primary/40 text-primary"
                      : e.enabled && e.version_id !== e.confirmed_version_id
                        ? "border-amber-500/40 text-amber-600 dark:text-amber-500"
                        : "",
                  )}
                >
                  {!e.enabled
                    ? t("未启用")
                    : e.version_id !== e.confirmed_version_id
                      ? t("生效中")
                      : e.version_id
                        ? t("已发布")
                        : t("未发布")}
                </Badge>
              </div>
              <div className="my-3 text-sm">
                {t("Version")} r{e.deployment_revision}{" "}
                <span className="text-muted-foreground">· {shortID(e.version_id)}</span>
              </div>
              <div className="grid gap-1">
                {(e.hosts || []).map((host, i) => {
                  const d = domains.find((x) => x.id === (e.domains || [])[i]);
                  const text = d?.label ? `${host}（${d.label}）` : host;
                  return e.version_id && e.enabled ? (
                    <a
                      key={host}
                      href={workerURL(host)}
                      target="_blank"
                      rel="noreferrer"
                      className="inline-flex items-center gap-1 break-all text-[11px] text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground"
                      onClick={(event) => event.stopPropagation()}
                    >
                      {text}
                      <ArrowUpRight
                        size={11}
                        className="shrink-0"
                      />
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
      </section>
      {env ? (
        <Tabs
          value={tab}
          onValueChange={setTab}
          className="min-w-0"
        >
          <div className="max-w-full overflow-x-auto">
            <TabsList>
              <TabsTrigger value="delivery">{t("构建与发布")}</TabsTrigger>
              <TabsTrigger value="versions">{t("版本与部署")}</TabsTrigger>
              <TabsTrigger value="source">{t("代码源")}</TabsTrigger>
              <TabsTrigger value="routing">{t("路由与域名")}</TabsTrigger>
              <TabsTrigger value="configurations">{t("配置版本")}</TabsTrigger>
              <TabsTrigger value="invoke">{t("调用测试")}</TabsTrigger>
            </TabsList>
          </div>
          <TabsContent
            value="delivery"
            forceMount
            className="mt-5 grid gap-5 data-[state=inactive]:hidden"
          >
            <LocalUpload
              base={base}
              worker={worker.name}
              env={env}
              builds={data.builds}
              canPublish={(name) =>
                !busy &&
                !deploymentPending(name) &&
                data.environments.some((e) => e.name === name && e.enabled)
              }
              onQueued={reload}
              onPublish={(build, name) => {
                setEnvironment(name);
                setPublish(build);
              }}
            />
            {tab === "delivery" ? (
              <>
                {!binding?.source ? (
                  <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-primary/25 bg-primary/5 px-4 py-3 text-sm">
                    <p className="text-muted-foreground">
                      {t("此Worker尚未绑定代码仓库；绑定后可直接从分支或 Tag 构建版本。")}
                    </p>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setTab("source")}
                    >
                      {t("绑定代码仓库")}
                    </Button>
                  </div>
                ) : null}
                <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
                  {Object.entries(artifactIcons).map(([label, Icon]) => (
                    <span
                      key={label}
                      className={cn("inline-flex items-center gap-1.5", artifactColors[label])}
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
                              {b.entrypoint} · {new Date(b.created_at).toLocaleString()}
                            </p>
                            {b.repository_source ? (
                              <p className="mt-1 text-xs text-muted-foreground">
                                {b.repository_source.repository} · {b.repository_source.ref} ·{" "}
                                {b.repository_source.commit?.slice(0, 12) || t("等待解析 Commit")}
                              </p>
                            ) : null}
                          </TableCell>
                          <TableCell>
                            <Status state={b.state} />
                            {b.error ? (
                              <p className="mt-1 max-w-xs text-xs text-destructive">{b.error}</p>
                            ) : null}
                          </TableCell>
                          <TableCell>
                            <VersionSummary
                              build={b}
                              data={data}
                              onSelect={(name) => setEnvironment(name)}
                            />
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              <Button
                                variant="outline"
                                className="border-primary/20 bg-primary/5 text-primary shadow-none hover:border-primary/40 hover:bg-primary/10 hover:text-primary"
                                size="sm"
                                disabled={!!inspectingBuild}
                                onClick={() => void inspectBuild(b)}
                              >
                                {inspectingBuild === b.id ? (
                                  <LoaderCircle
                                    size={14}
                                    className="animate-spin"
                                  />
                                ) : (
                                  <FileText size={14} />
                                )}
                                {inspectingBuild === b.id ? t("加载中…") : t("日志与制品")}
                              </Button>
                              <Button
                                size="sm"
                                className="shadow-none"
                                disabled={b.state !== "succeeded" || active || !env.enabled}
                                onClick={() => setPublish(b)}
                              >
                                <Rocket size={14} />
                                {t("发布")}
                              </Button>
                              <BuildVersionMenu
                                disabled={b.state !== "succeeded" || busy || !env.enabled}
                                create={() => setVersionDraft({ build: b, env })}
                              />
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
                            {t("还没有构建。")}
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </TableBody>
                  </Table>
                </Card>
                {operation ? (
                  <OperationPanel
                    operation={operation}
                    operations={operations.filter((o) => o.environment === env.name)}
                    select={setSelectedOperation}
                  />
                ) : null}
                <Card className="py-0 shadow-none">
                  <CardContent className="p-5">
                    <h3 className="mb-4 font-medium">
                      {env.name} · {t("发布历史")}
                    </h3>
                    {(data.histories[env.name] || []).length === 0 ? (
                      <p className="text-sm text-muted-foreground">{t("尚无成功生效的发布。")}</p>
                    ) : (
                      (data.histories[env.name] || []).map((h) => (
                        <div
                          key={h.operation_id}
                          className="flex flex-wrap items-center justify-between gap-3 border-t py-3 first:border-0"
                        >
                          <div>
                            <span className="font-medium">r{h.revision}</span>{" "}
                            <code className="ml-2 text-xs">{shortID(h.version_id)}</code>
                            <p className="mt-1 text-xs text-muted-foreground">
                              {h.kind} · {new Date(h.time).toLocaleString()}
                            </p>
                          </div>
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={
                              rollbackBusy || !env.enabled || env.version_id === h.version_id
                            }
                            onClick={() =>
                              setRollback({
                                id: h.version_id,
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
              </>
            ) : null}
          </TabsContent>
          <TabsContent
            value="versions"
            className="mt-5"
          >
            <VersionsPanel
              key={base + "/" + env.name}
              base={base}
              env={env}
              data={data}
              reload={reload}
              notify={notify}
              createdVersion={createdVersion}
              operations={operations}
              operation={operation}
              selectOperation={setSelectedOperation}
              deployed={(op) => {
                setAcceptedOperation(op);
                setSelectedOperation(op.id);
              }}
            />
          </TabsContent>
          <TabsContent
            value="source"
            className="mt-5 grid gap-3"
          >
            <h3 className="flex items-center gap-2 text-sm font-medium">{t("代码仓库")}</h3>
            <RepositorySettings
              base={base}
              organizationBase={organizationBase}
              bound={binding?.source ?? null}
              authorizationStatus={binding?.authorization_status}
              revision={binding?.revision ?? 0}
              onSaved={setBinding}
              notify={notify}
            />
          </TabsContent>
          <TabsContent
            value="routing"
            className="mt-5 grid gap-5"
          >
            <WorkerDomains
              base={base}
              domains={domains}
              networkProfiles={networkProfiles}
              currentProfile={routing?.network_profile}
              environments={data.environments}
              revision={routing?.revision ?? data.domainsRevision}
              active={active}
              reload={reload}
              notify={notify}
            />
          </TabsContent>
          <TabsContent
            value="configurations"
            className="mt-5 grid gap-6"
          >
            <ConfigurationsPanel
              key={env.name}
              base={base}
              organizationBase={organizationBase}
              env={env}
              data={data}
              active={active}
              reload={reload}
              notify={notify}
              operations={operations}
              operation={operation}
              selectOperation={setSelectedOperation}
              deployed={(op) => {
                setAcceptedOperation(op);
                setSelectedOperation(op.id);
              }}
            />
            <details className="rounded-lg border p-4">
              <summary className="cursor-pointer text-sm font-medium">{t("管理环境草稿")}</summary>
              <p className="mt-2 text-xs text-muted-foreground">
                {t("草稿用于下次从构建发布；可先编辑，再从草稿保存一个命名配置版本。")}
              </p>
              <div className="mt-4">
                <EnvironmentSettings
                  key={env.name}
                  base={base}
                  organizationBase={organizationBase}
                  env={env}
                  active={active}
                  data={data}
                  domains={domains}
                  reload={reload}
                  notify={notify}
                />
              </div>
            </details>
          </TabsContent>
          <TabsContent
            value="invoke"
            className="mt-5"
          >
            <Invocation
              key={env.name}
              base={base}
              env={env}
            />
          </TabsContent>
        </Tabs>
      ) : null}
      <Dialog
        open={previewDialog}
        onOpenChange={setPreviewDialog}
      >
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t("创建预览环境")}</DialogTitle>
            <DialogDescription>
              {t("预览环境是临时的验证场所，验证通过后可将同一制品晋升到基线。")}
            </DialogDescription>
          </DialogHeader>
          <Field
            label={t("预览名称")}
            hint={t("小写字母、数字、连字符，例如 fix-auth。")}
          >
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
          <details className="min-w-0 rounded-lg border p-4">
            <summary className="cursor-pointer text-sm font-medium">
              {t("新建预览默认配置")}
            </summary>
            <div className="mt-4">
              <PreviewBasePanel
                base={base}
                organizationBase={organizationBase}
                notify={notify}
              />
            </div>
          </details>
          <Button
            disabled={
              busy ||
              !/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(previewName) ||
              previewName === "production"
            }
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
      {versionDraft ? (
        <CreateVersionDialog
          base={base}
          build={versionDraft.build}
          env={versionDraft.env}
          close={() => {
            setVersionDraft(null);
            reload();
          }}
          created={(version) => {
            setVersionDraft(null);
            setCreatedVersion(version);
            setEnvironment(version.environment);
            setTab("versions");
            reload();
          }}
        />
      ) : null}
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
            <SelectField
              value={env?.name || ""}
              onChange={setEnvironment}
            >
              {data.environments
                .filter((e) => e.enabled)
                .map((e) => (
                  <option
                    key={e.name}
                    value={e.name}
                  >
                    {e.name}
                  </option>
                ))}
            </SelectField>
          </Field>
          <p className="text-sm">
            {shortID(publish?.id)} → {env?.name} / r{env?.deployment_revision}
          </p>
          <pre className="max-h-40 overflow-auto rounded-md bg-secondary p-3 text-xs">
            {JSON.stringify(env?.variables, null, 2)}
          </pre>
          <Button
            disabled={busy || active || !env?.enabled}
            onClick={() => {
              if (publish) void submitVersion(publish);
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
              {t("恢复该历史 Version 的代码和变量，仍需验证并等待生效。")}
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
                    version_id: rollback.id,
                    expected_revision: env.deployment_revision,
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
      {buildInfo ? (
        <BuildInspector
          key={buildInfo.build.id}
          info={buildInfo}
          onClose={() => setBuildInfo(null)}
          notify={notify}
        />
      ) : null}
    </div>
  );
}

// Per-build version chips: only environments this build reached appear,
// colored by the shared version state system.
