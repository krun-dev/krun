import { useEffect, useRef, useState, type ChangeEvent } from "react";
import { GitBranch, LoaderCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "@/lib/api";
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
import type {
  Build,
  RepositoryBinding,
  RepositoryProvider,
  RepositorySource,
} from "@/lib/workers";

const emptySource: RepositorySource = {
  provider_id: "",
  repository: "",
  ref: "HEAD",
  root_directory: ".",
  entrypoint: "src/index.ts",
};

export function errorMessage(e: unknown): string {
  if (e instanceof APIError) return e.message;
  return String(e);
}

function useProviders(organizationBase: string, open: boolean) {
  const [providers, setProviders] = useState<RepositoryProvider[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setProviders(null);
    setError("");
    void api<{ items: RepositoryProvider[] }>(
      organizationBase + "/workers/repository-providers",
      "GET",
      undefined,
      controller.signal,
    )
      .then((p) => {
        if (!controller.signal.aborted) setProviders(p.items);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      });
    return () => controller.abort();
  }, [open, organizationBase]);
  return { providers, setProviders, error, setError };
}

// ---------------------------------------------------------------------------
// Settings panel: bind the repository once (provider, repo, directory,
// entrypoint) and manage the per-user connection. Lives in the settings tab.
// ---------------------------------------------------------------------------
export function RepositorySettings({
  base,
  organizationBase,
  bound,
  revision,
  onSaved,
  notify,
}: {
  base: string;
  organizationBase: string;
  bound: RepositorySource | null;
  revision: number;
  onSaved: (binding: RepositoryBinding) => void;
  notify: (message: string) => void;
}) {
  const { t } = useTranslation();
  const { providers, setProviders, error, setError } = useProviders(
    organizationBase,
    true,
  );
  const [source, setSource] = useState<RepositorySource>(
    bound || emptySource,
  );
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setSource(bound || emptySource);
  }, [bound]);
  const provider = providers?.find((p) => p.id === source.provider_id);
  function update(key: keyof RepositorySource, value: string) {
    setSource((previous) => ({ ...previous, [key]: value }));
  }
  async function toggleConnection() {
    if (!provider) return;
    setBusy(true);
    try {
      if (provider.connected) {
        await api(
          organizationBase +
            "/workers/repository/" +
            encodeURIComponent(provider.id) +
            "/disconnect",
          "DELETE",
        );
        setProviders((previous) =>
          previous
            ? previous.map((p) =>
                p.id === provider.id ? { ...p, connected: false } : p,
              )
            : previous,
        );
        notify(t("已断开仓库账号连接。"));
      } else {
        const { authorization_url } = await api<{ authorization_url: string }>(
          organizationBase +
            "/workers/repository/" +
            encodeURIComponent(provider.id) +
            "/connect",
          "POST",
        );
        window.location.assign(authorization_url);
      }
    } catch (e) {
      setError(errorMessage(e));
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
    return (
      <p className="text-sm text-muted-foreground">
        {t("管理员尚未启用代码仓库来源。")}
      </p>
    );
  return (
    <div className="grid gap-4">
      {error ? (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      ) : null}
      {providers === null && !error ? <p className="text-sm">{t("加载中…")}</p> : null}
      {providers && providers.length > 0 ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <SelectField
              label={t("仓库平台")}
              disabled={busy}
              value={source.provider_id}
              onChange={(value) => update("provider_id", value)}
            >
              {!source.provider_id ? (
                <option value="" disabled>
                  {t("选择仓库平台")}
                </option>
              ) : null}
              {!provider && source.provider_id ? (
                <option value={source.provider_id} disabled>
                  {t("当前仓库来源已停用")}
                </option>
              ) : null}
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
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
                placeholder="team/worker-service"
                maxLength={500}
                disabled={busy}
              />
            </Field>
          </div>
          {provider ? (
            <div className="flex items-center justify-between gap-3 rounded-md border bg-background/50 px-3 py-2.5">
              <p className="text-xs text-muted-foreground">
                {provider.connected
                  ? t("已连接你的账号，私有仓库按你的权限可见。")
                  : provider.oauth
                    ? t("连接你的账号后可构建你有权限的私有仓库。")
                    : t("公开仓库匿名拉取；私有仓库需管理员配置共享凭据。")}
              </p>
              {provider.oauth ? (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() => void toggleConnection()}
                >
                  {provider.connected ? t("断开") : t("连接账号")}
                </Button>
              ) : null}
            </div>
          ) : null}
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
            <Field
              label={t("入口文件")}
              hint={t("相对项目目录，例如 src/index.ts。")}
            >
              <Input
                aria-label={t("入口文件")}
                value={source.entrypoint}
                onChange={(e: ChangeEvent<HTMLInputElement>) =>
                  update("entrypoint", e.target.value)
                }
                maxLength={160}
                disabled={busy}
              />
            </Field>
          </div>
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
              disabled={
                busy || !provider || !source.repository || !source.entrypoint
              }
              onClick={() => void save()}
            >
              {busy ? <LoaderCircle size={16} className="animate-spin" /> : null}
              {t("保存仓库配置")}
            </Button>
          </div>
        </>
      ) : null}
    </div>
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
      // Builds are service-level: the artifact is environment-independent and
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
            {bound
              ? `${bound.provider_id} · ${bound.repository}`
              : t("先在设置中绑定仓库。")}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p role="alert" className="text-sm text-destructive">
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
                onChange={(e: ChangeEvent<HTMLInputElement>) =>
                  setRef(e.target.value)
                }
                placeholder="HEAD"
                maxLength={200}
                disabled={busy}
              />
            </Field>
            <div className="flex justify-end">
              <Button type="submit" disabled={busy}>
                {busy ? (
                  <LoaderCircle size={16} className="animate-spin" />
                ) : (
                  <GitBranch size={16} />
                )}
                {t("开始构建")}
              </Button>
            </div>
          </form>
        ) : (
          <p className="text-sm text-muted-foreground">
            {t("此服务尚未绑定代码仓库，请先在“环境配置”中完成绑定。")}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}
