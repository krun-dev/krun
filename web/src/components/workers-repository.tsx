import { useEffect, useRef, useState } from "react";
import { GitBranch, LoaderCircle } from "lucide-react";
import { api, type List } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import type {
  Build,
  Environment,
  RepositoryProvider,
  RepositorySource,
} from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Field, Loading, SelectField } from "@/components/common";

type RepositoryBinding = { source: RepositorySource | null; revision: number };
const emptySource: RepositorySource = {
  provider_id: "",
  repository: "",
  ref: "HEAD",
  root_directory: ".",
  entrypoint: "src/index.ts",
};

export function RepositoryBuild({
  open,
  close,
  base,
  organizationBase,
  environments,
  selectedEnvironment,
  done,
  notify,
}: {
  open: boolean;
  close: () => void;
  base: string;
  organizationBase: string;
  environments: Environment[];
  selectedEnvironment: string;
  done: () => void;
  notify: (message: string) => void;
}) {
  const [providers, setProviders] = useState<RepositoryProvider[] | null>(null);
  const [binding, setBinding] = useState<RepositoryBinding>({
    source: null,
    revision: 0,
  });
  const [source, setSource] = useState<RepositorySource>(emptySource);
  const [environment, setEnvironment] = useState(selectedEnvironment);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef({ fingerprint: "", key: "" });
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setProviders(null);
    setError("");
    setEnvironment(selectedEnvironment);
    void Promise.all([
      api<List<RepositoryProvider>>(
        organizationBase + "/workers/repository-providers",
        "GET",
        undefined,
        controller.signal,
      ),
      api<RepositoryBinding>(
        base + "/repository",
        "GET",
        undefined,
        controller.signal,
      ),
    ])
      .then(([p, b]) => {
        if (controller.signal.aborted) return;
        setProviders(p.items);
        setBinding(b);
        setSource(
          b.source || { ...emptySource, provider_id: p.items[0]?.id || "" },
        );
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      });
    return () => controller.abort();
  }, [open, base, organizationBase, selectedEnvironment]);
  const provider = providers?.find((p) => p.id === source.provider_id);
  const enabledEnvironment = environments.some(
    (e) => e.name === environment && e.enabled,
  );
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
  async function saveRepository() {
    if (JSON.stringify(binding.source) === JSON.stringify(source))
      return binding;
    const result = await api<RepositoryBinding>(base + "/repository", "PUT", {
      source,
      expected_revision: binding.revision,
    });
    setBinding(result);
    if (result.source) setSource(result.source);
    return result;
  }
  async function submit(build: boolean) {
    setBusy(true);
    setError("");
    try {
      const saved = await saveRepository();
      if (!build) {
        notify(t("仓库配置已保存。"));
        return;
      }
      const payload = {
        source_type: "repository",
        repository_revision: saved.revision,
        environment,
      };
      const fingerprint = JSON.stringify({ ...payload, source: saved.source });
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
      onOpenChange={(value) => {
        if (!value && !busy) close();
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <GitBranch size={18} />
            {t("从仓库构建")}
          </DialogTitle>
          <DialogDescription>
            {t("指定仓库和分支，平台拉取固定 Commit 的代码并生成版本制品。")}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {providers === null ? (
          !error ? (
            <Loading />
          ) : null
        ) : providers.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("管理员尚未启用代码仓库来源。")}
          </p>
        ) : (
          <form
            className="grid gap-5"
            onSubmit={(event) => {
              event.preventDefault();
              void submit(true);
            }}
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <SelectField
                label={t("仓库平台")}
                disabled={busy}
                value={source.provider_id}
                onChange={(value) => update("provider_id", value)}
              >
                {!provider ? (
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
              <SelectField
                label={t("构建环境")}
                disabled={busy}
                value={environment}
                onChange={setEnvironment}
              >
                {environments
                  .filter((e) => e.enabled)
                  .map((e) => (
                    <option key={e.name} value={e.name}>
                      {e.name}
                    </option>
                  ))}
              </SelectField>
            </div>
            {provider ? (
              <div className="-mt-1 flex items-center justify-between gap-3 rounded-md border bg-background/50 px-3 py-2.5">
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
            {provider ? (
              <p className="-mt-3 break-all text-xs text-muted-foreground">
                {provider.base_url}
              </p>
            ) : null}

            <Field
              label={t("仓库地址")}
              hint={t(
                "填写 namespace/repository 或所选平台的完整 HTTPS 仓库地址。",
              )}
            >
              <Input
                aria-label={t("仓库地址")}
                value={source.repository}
                onChange={(e) => update("repository", e.target.value)}
                placeholder="team/worker-service"
                required
                maxLength={500}
                disabled={busy}
              />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t("分支 / Tag / Commit")}
                hint={t("HEAD 表示默认分支；构建记录会保存实际 Commit。")}
              >
                <Input
                  aria-label={t("分支 / Tag / Commit")}
                  value={source.ref}
                  onChange={(e) => update("ref", e.target.value)}
                  placeholder="HEAD"
                  maxLength={200}
                  disabled={busy}
                />
              </Field>
              <Field
                label={t("项目目录")}
                hint={t("相对仓库根目录，使用 . 表示根目录。")}
              >
                <Input
                  aria-label={t("项目目录")}
                  value={source.root_directory}
                  onChange={(e) => update("root_directory", e.target.value)}
                  placeholder="."
                  required
                  maxLength={160}
                  disabled={busy}
                />
              </Field>
            </div>
            <Field
              label={t("入口文件")}
              hint={t("相对项目目录，例如 src/index.ts。")}
            >
              <Input
                aria-label={t("入口文件")}
                value={source.entrypoint}
                onChange={(e) => update("entrypoint", e.target.value)}
                required
                maxLength={160}
                disabled={busy}
              />
            </Field>
            <p className="rounded-md bg-secondary p-3 text-xs text-muted-foreground">
              {t(
                "本版本构建 JS / TS 和相对文件导入；暂不执行 npm 安装或仓库中的构建脚本。项目源码最多 32 个文件、256 KiB。",
              )}
            </p>
            <div className="flex justify-end gap-3">
              <Button
                type="button"
                variant="outline"
                disabled={
                  busy || !provider || !source.repository || !source.entrypoint
                }
                onClick={() => void submit(false)}
              >
                {t("保存仓库配置")}
              </Button>
              <Button
                type="submit"
                disabled={busy || !provider || !enabledEnvironment}
              >
                {busy ? (
                  <LoaderCircle size={16} className="animate-spin" />
                ) : (
                  <GitBranch size={16} />
                )}
                {t("开始构建")}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
