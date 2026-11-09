import { useCallback, useEffect, useRef, useState, type ChangeEvent } from "react";
import { GitBranch, LoaderCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "@/lib/api";
import { connectRepository, repositoryEndpoint } from "@/lib/repository-authorization";
import {
  repositoryAuthorizationError,
  repositoryStatusLabel,
  useRepositoryProviders,
} from "@/lib/repository-providers";
import { Field, SelectField } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { Build, RepositoryBinding, RepositoryProvider, RepositorySource } from "@/lib/workers";

const emptySource: RepositorySource = {
  provider_id: "",
  repository: "",
  ref: "HEAD",
  root_directory: ".",
};

export function errorMessage(e: unknown): string {
  if (e instanceof APIError) return e.message;
  return String(e);
}

// ---------------------------------------------------------------------------
// Settings panel: bind the repository once (provider, repo and project
// directory) and manage the per-user connection. Wrangler owns build inputs.
// ---------------------------------------------------------------------------
export function RepositorySettings({
  base,
  organizationBase,
  bound,
  authorizationStatus,
  revision,
  onSaved,
  notify,
}: {
  base: string;
  organizationBase: string;
  bound: RepositorySource | null;
  authorizationStatus?: RepositoryBinding["authorization_status"];
  revision: number;
  onSaved: (binding: RepositoryBinding) => void;
  notify: (message: string) => void;
}) {
  const { t } = useTranslation();
  const { providers, setProviders, error, setError } = useRepositoryProviders(organizationBase);
  const [picker, setPicker] = useState(false);
  const [useAuthorization, setUseAuthorization] = useState(false);
  const [resumeProvider, setResumeProvider] = useState(
    () => new URLSearchParams(location.search).get("repository_pick") || "",
  );
  const [source, setSource] = useState<RepositorySource>(bound || emptySource);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setSource(bound || emptySource);
    setUseAuthorization(false);
  }, [bound]);
  const provider = providers?.find((p) => p.id === source.provider_id);
  useEffect(() => {
    if (!resumeProvider || !providers) return;
    const url = new URL(location.href);
    url.searchParams.delete("repository_pick");
    history.replaceState(null, "", url);
    setResumeProvider("");
    if (providers.some((item) => item.id === resumeProvider && item.connected)) {
      setSource((previous) => ({ ...previous, provider_id: resumeProvider }));
      setUseAuthorization(false);
      setPicker(true);
    }
  }, [resumeProvider, providers]);

  function update(key: keyof RepositorySource, value: string) {
    if (key === "provider_id" || key === "repository") {
      setUseAuthorization(false);
    }
    setSource((previous) => ({ ...previous, [key]: value }));
  }

  const markReconnectRequired = useCallback(
    (id: string) => {
      setProviders(
        (previous) =>
          previous?.map((item) =>
            item.id === id
              ? { ...item, connected: false, authorization_status: "reconnect_required" }
              : item,
          ) ?? null,
      );
    },
    [setProviders],
  );

  async function authorize() {
    if (!provider) return;
    setBusy(true);
    setError("");
    try {
      await connectRepository(organizationBase, provider.id);
    } catch (error) {
      setPicker(false);
      setError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    setBusy(true);
    setError("");
    try {
      const result = await api<RepositoryBinding>(base + "/repository", "PUT", {
        source,
        expected_revision: revision,
        use_authorization: useAuthorization,
      });
      onSaved(result);
      notify(t("仓库配置已保存。"));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function unbind() {
    setBusy(true);
    setError("");
    try {
      const result = await api<RepositoryBinding>(base + "/repository", "PUT", {
        source: null,
        expected_revision: revision,
      });
      onSaved(result);
      setSource(emptySource);
      notify(t("已解除仓库绑定。"));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  if (providers !== null && providers.length === 0)
    return <p className="text-sm text-muted-foreground">{t("管理员尚未启用代码仓库来源。")}</p>;
  return (
    <div className="grid gap-4">
      {authorizationStatus === "reconnect_required" && (
        <p
          role="status"
          className="text-sm text-destructive"
        >
          {t("当前绑定的仓库授权已失效。请由授权人重新授权，或使用你的授权重新绑定仓库。")}
        </p>
      )}
      {error ? (
        <p
          role="alert"
          className="text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {providers === null && !error ? <p className="text-sm">{t("加载中…")}</p> : null}
      {providers && providers.length > 0 ? (
        <>
          <div>
            <SelectField
              label={t("仓库平台")}
              disabled={busy}
              value={source.provider_id}
              onChange={(value) => update("provider_id", value)}
            >
              {!source.provider_id ? (
                <option
                  value=""
                  disabled
                >
                  {t("选择仓库平台")}
                </option>
              ) : null}
              {!provider && source.provider_id ? (
                <option
                  value={source.provider_id}
                  disabled
                >
                  {t("当前仓库来源已停用")}
                </option>
              ) : null}
              {providers.map((p) => (
                <option
                  key={p.id}
                  value={p.id}
                >
                  {p.name}
                </option>
              ))}
            </SelectField>
            <Field
              label={t("仓库地址")}
              hint={t("填写 namespace/repository 或完整 HTTPS 仓库地址。")}
            >
              <Input
                aria-label={t("仓库地址")}
                value={source.repository}
                onChange={(e: ChangeEvent<HTMLInputElement>) =>
                  update("repository", e.target.value)
                }
                placeholder="team/worker-worker"
                maxLength={500}
                disabled={busy}
              />
            </Field>
          </div>
          {provider && (
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border bg-background/50 px-3 py-2.5">
              <p className="text-xs text-muted-foreground">
                {provider.oauth
                  ? t(
                      repositoryStatusLabel[provider.authorization_status] ??
                        (provider.connected ? "已授权" : "未授权"),
                    )
                  : t("公开仓库匿名拉取；私有仓库需管理员配置共享凭据。")}
              </p>
              <div className="flex flex-wrap gap-2">
                {provider.oauth &&
                  (provider.connected ? (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy}
                      onClick={() => setPicker(true)}
                    >
                      {t("选择已授权仓库")}
                    </Button>
                  ) : (
                    <Button
                      type="button"
                      size="sm"
                      disabled={busy}
                      onClick={() => void authorize()}
                    >
                      {t(
                        provider.authorization_status === "reconnect_required"
                          ? "重新授权"
                          : provider.type === "github"
                            ? "连接 GitHub"
                            : "连接账号",
                      )}
                    </Button>
                  ))}
              </div>
            </div>
          )}
          {useAuthorization && (
            <p className="text-xs text-muted-foreground">
              {t("保存后，有构建权限的成员可以通过此授权构建该 Worker 的绑定仓库。")}
            </p>
          )}
          {picker && provider && (
            <RepositoryPicker
              key={provider.id}
              organizationBase={organizationBase}
              provider={provider}
              close={() => setPicker(false)}
              authorize={() => void authorize()}
              busy={busy}
              onExpired={markReconnectRequired}
              select={(repository) => {
                setSource((previous) => ({ ...previous, repository }));
                setUseAuthorization(true);
                setPicker(false);
              }}
            />
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label={t("项目目录")}
              hint={t("相对仓库根目录，使用 . 表示根目录。")}
            >
              <Input
                aria-label={t("项目目录")}
                value={source.root_directory}
                onChange={(e: ChangeEvent<HTMLInputElement>) =>
                  update("root_directory", e.target.value)
                }
                placeholder="."
                maxLength={160}
                disabled={busy}
              />
            </Field>
          </div>
          <p className="text-xs text-muted-foreground">
            {t("入口、静态资源和构建命令读取项目内的 Wrangler 配置。")}
          </p>
          <div className="flex justify-end gap-3">
            {bound ? (
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => void unbind()}
              >
                {t("解除绑定")}
              </Button>
            ) : null}
            <Button
              type="button"
              disabled={busy || !provider || !source.repository}
              onClick={() => void save()}
            >
              {busy ? (
                <LoaderCircle
                  size={16}
                  className="animate-spin"
                />
              ) : null}
              {t("保存仓库配置")}
            </Button>
          </div>
        </>
      ) : null}
    </div>
  );
}

type AuthorizedRepository = { name: string; private: boolean };

function RepositoryPicker({
  organizationBase,
  provider,
  close,
  authorize,
  busy,
  onExpired,
  select,
}: {
  organizationBase: string;
  provider: RepositoryProvider;
  close: () => void;
  authorize: () => void;
  busy: boolean;
  onExpired: (provider: string) => void;
  select: (repository: string) => void;
}) {
  const { t } = useTranslation();
  const [items, setItems] = useState<AuthorizedRepository[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [reconnect, setReconnect] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    void api<{ items: AuthorizedRepository[]; has_more: boolean }>(
      repositoryEndpoint(organizationBase, provider.id) + "/repositories",
      "POST",
      { page },
      controller.signal,
    )
      .then((result) => {
        if (controller.signal.aborted) return;
        setItems((current) => (page === 1 ? result.items : [...current, ...result.items]));
        setHasMore(result.has_more);
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (repositoryAuthorizationError(error)) {
          onExpired(provider.id);
          setReconnect(true);
        } else if (error instanceof APIError && error.code === "WORKER_REPOSITORY_LIST_DENIED") {
          setReconnect(true);
        }
        setError(errorMessage(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [organizationBase, provider.id, page, attempt, onExpired]);

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) close();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("选择已授权仓库")}</DialogTitle>
          <DialogDescription>
            {provider.name} · {t("仅显示当前账号可用的仓库。")}
          </DialogDescription>
        </DialogHeader>
        {error && (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {error}
          </p>
        )}
        <div className="grid max-h-[45vh] gap-1 overflow-y-auto">
          {items.map((item) => (
            <Button
              key={item.name}
              type="button"
              variant="ghost"
              className="justify-between"
              onClick={() => select(item.name)}
            >
              <span className="truncate">{item.name}</span>
              <span className="text-xs text-muted-foreground">
                {t(item.private ? "私有" : "公开")}
              </span>
            </Button>
          ))}
        </div>
        {!items.length && !loading && !error && (
          <p className="text-sm text-muted-foreground">
            {t("没有可用仓库，请检查 Git 平台授权范围。")}
          </p>
        )}
        {loading && (
          <p
            role="status"
            className="text-sm"
          >
            {t("加载中…")}
          </p>
        )}
        {provider.install_url && !loading && provider.connected && (
          <details className="text-sm text-muted-foreground">
            <summary className="cursor-pointer">{t("找不到仓库？")}</summary>
            <p className="mt-2">
              {t(
                "请确认你的 GitHub 账号能访问该仓库，并且仓库已开放给 GitHub App。组织仓库可能需要管理员授权。",
              )}
            </p>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                asChild
              >
                <a
                  href={provider.install_url}
                  target="_blank"
                  rel="noreferrer"
                >
                  {t("安装 GitHub App 或调整仓库范围")}
                </a>
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => {
                  setItems([]);
                  setHasMore(false);
                  setPage(1);
                  setAttempt((current) => current + 1);
                }}
              >
                {t("刷新仓库列表")}
              </Button>
            </div>
          </details>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          {reconnect ? (
            <Button
              type="button"
              disabled={busy}
              onClick={authorize}
            >
              {t("重新授权")}
            </Button>
          ) : error ? (
            <Button
              type="button"
              variant="outline"
              onClick={() => setAttempt((n) => n + 1)}
            >
              {t("重试")}
            </Button>
          ) : (
            hasMore && (
              <Button
                type="button"
                variant="outline"
                disabled={loading}
                onClick={() => setPage((n) => n + 1)}
              >
                {t("加载更多")}
              </Button>
            )
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Build dialog: with a bound repository only the ref and target environment
// remain per-build inputs.
// ---------------------------------------------------------------------------
export function RepositoryBuild({
  open,
  close,
  base,
  bound,
  repositoryRevision,
  done,
  notify,
}: {
  open: boolean;
  close: () => void;
  base: string;
  bound: RepositorySource | null;
  repositoryRevision: number;
  done: () => void;
  notify: (message: string) => void;
}) {
  const { t } = useTranslation();
  const [ref, setRef] = useState(bound?.ref || "HEAD");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef({ fingerprint: "", key: "" });
  useEffect(() => {
    if (open) {
      setRef(bound?.ref || "HEAD");
      setError("");
    }
  }, [open, bound]);
  async function submit() {
    setBusy(true);
    setError("");
    try {
      // Builds are worker-level: the artifact is environment-independent and
      // the target environment is chosen at publish time. The backend still
      // anchors build permission to a baseline environment.
      const payload = {
        source_type: "repository",
        repository_revision: repositoryRevision,
        environment: "production",
        ref,
      };
      const fingerprint = JSON.stringify({ ...payload, bound });
      if (attempt.current.fingerprint !== fingerprint) {
        attempt.current = { fingerprint, key: crypto.randomUUID() };
      }
      await api<Build>(base + "/builds", "POST", {
        ...payload,
        idempotency_key: attempt.current.key,
      });
      attempt.current = { fingerprint: "", key: "" };
      notify(t("仓库构建已提交，正在拉取源码。"));
      done();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(value: boolean) => {
        if (!value && !busy) close();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <GitBranch size={18} />
            {t("从仓库构建")}
          </DialogTitle>
          <DialogDescription>
            {bound ? `${bound.provider_id} · ${bound.repository}` : t("先在设置中绑定仓库。")}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {error}
          </p>
        ) : null}
        {bound ? (
          <form
            className="grid gap-5"
            onSubmit={(event) => {
              event.preventDefault();
              void submit();
            }}
          >
            <Field
              label={t("分支 / Tag / Commit")}
              hint={t("HEAD 表示默认分支；构建记录会保存实际 Commit。")}
            >
              <Input
                aria-label={t("分支 / Tag / Commit")}
                value={ref}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setRef(e.target.value)}
                placeholder="HEAD"
                maxLength={200}
                disabled={busy}
              />
            </Field>
            <div className="flex justify-end">
              <Button
                type="submit"
                disabled={busy}
              >
                {busy ? (
                  <LoaderCircle
                    size={16}
                    className="animate-spin"
                  />
                ) : (
                  <GitBranch size={16} />
                )}
                {t("开始构建")}
              </Button>
            </div>
          </form>
        ) : (
          <p className="text-sm text-muted-foreground">
            {t("此Worker尚未绑定代码仓库，请先在“环境配置”中完成绑定。")}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}
