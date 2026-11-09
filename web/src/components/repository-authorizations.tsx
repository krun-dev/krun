import { useState } from "react";
import { GitBranch } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "@/lib/api";
import { errorMessage } from "@/lib/i18n";
import { connectRepository, repositoryEndpoint } from "@/lib/repository-authorization";
import type { RepositoryProvider } from "@/lib/workers";
import {
  repositoryAuthorizationError,
  repositoryStatusLabel,
  useRepositoryProviders,
} from "@/lib/repository-providers";
import { Empty, Loading, PageHeader } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

export default function RepositoryAuthorizations({
  organizationBase,
  notify,
}: {
  organizationBase: string;
  notify: (message: string) => void;
}) {
  const { t } = useTranslation();
  const { providers, setProviders, error, setError, reload } =
    useRepositoryProviders(organizationBase);
  const [busy, setBusy] = useState("");
  const [disconnect, setDisconnect] = useState("");
  async function act(provider: RepositoryProvider, action: "connect" | "verify" | "disconnect") {
    setBusy(provider.id);
    setError("");
    try {
      if (action === "connect") {
        await connectRepository(organizationBase, provider.id);
      } else if (action === "disconnect") {
        await api(repositoryEndpoint(organizationBase, provider.id) + "/disconnect", "DELETE");
        setDisconnect("");
        reload();
        notify(t("已断开仓库账号连接。"));
      } else {
        await api(repositoryEndpoint(organizationBase, provider.id) + "/repositories", "POST", {
          page: 1,
        });
        reload();
        notify(t("仓库授权验证成功。"));
      }
    } catch (error) {
      if (repositoryAuthorizationError(error)) {
        setProviders(
          (current) =>
            current?.map((item) =>
              item.id === provider.id
                ? { ...item, connected: false, authorization_status: "reconnect_required" }
                : item,
            ) ?? null,
        );
      }
      setError(errorMessage(error));
    } finally {
      setBusy("");
    }
  }

  const sources = providers?.filter((provider) => provider.oauth) ?? [];
  return (
    <>
      <PageHeader
        eyebrow={t("个人设置")}
        title={t("Git 仓库授权")}
        description={t("管理当前账号的 GitHub 和 GitLab 仓库授权。")}
        action={
          <Button
            variant="outline"
            onClick={reload}
            disabled={Boolean(busy)}
          >
            {t("刷新")}
          </Button>
        }
      />
      {error && (
        <p
          role="alert"
          className="mb-4 text-sm text-destructive"
        >
          {error}
        </p>
      )}
      {providers === null && !error && <Loading />}
      {providers && sources.length === 0 && (
        <Empty
          title={t("暂无可授权的仓库平台")}
          description={t("请联系管理员启用仓库账号授权。")}
        />
      )}
      <div className="grid gap-3">
        {sources.map((provider) => {
          const status = provider.authorization_status;
          return (
            <section
              key={provider.id}
              className="rounded-md border bg-card p-4"
            >
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <div className="flex items-center gap-2">
                    <GitBranch size={16} />
                    <h2 className="text-sm font-medium">{provider.name}</h2>
                    <Badge variant="outline">{t(repositoryStatusLabel[status])}</Badge>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{provider.base_url}</p>
                </div>
                <div className="flex flex-wrap gap-2">
                  {provider.connected && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={Boolean(busy)}
                      onClick={() => void act(provider, "verify")}
                    >
                      {t("验证授权")}
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant={provider.connected ? "outline" : "default"}
                    disabled={Boolean(busy)}
                    onClick={() => void act(provider, "connect")}
                  >
                    {t(
                      status !== "not_connected"
                        ? "重新授权"
                        : provider.type === "github"
                          ? "连接 GitHub"
                          : "连接账号",
                    )}
                  </Button>
                  {status !== "not_connected" && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={Boolean(busy)}
                      onClick={() => setDisconnect(provider.id)}
                    >
                      {t("断开")}
                    </Button>
                  )}
                </div>
              </div>
              {disconnect === provider.id && (
                <div className="mt-4 flex flex-wrap items-center gap-3 border-t pt-3">
                  <p className="text-sm text-muted-foreground">
                    {t("使用此授权的 Worker 将无法发起新的仓库构建，直至重新授权。")}
                  </p>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setDisconnect("")}
                  >
                    {t("取消")}
                  </Button>
                  <Button
                    variant="destructive"
                    size="sm"
                    disabled={Boolean(busy)}
                    onClick={() => void act(provider, "disconnect")}
                  >
                    {t("确认断开")}
                  </Button>
                </div>
              )}
            </section>
          );
        })}
      </div>
    </>
  );
}
