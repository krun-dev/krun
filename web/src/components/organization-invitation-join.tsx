import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Building2, ShieldCheck } from "lucide-react";
import { api, APIError, type Me, type Provider } from "@/lib/api";
import { errorMessage, locale, roleLabel, t } from "@/lib/i18n";
import {
  captureOrganizationInvitation,
  clearOrganizationInvitation,
  type InvitationPreview,
  type InvitationResult,
} from "@/lib/organization-invitations";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { LanguageSwitch } from "./language";
import { Loading, Logo, ThemeToggle } from "./common";
import { Login } from "./login";

export function OrganizationInvitationJoin({
  me,
  providers,
  development,
}: {
  me: Me | null;
  providers: Provider[];
  development: boolean;
}) {
  useTranslation();
  const [invitation, setInvitation] = useState(captureOrganizationInvitation);
  const currentToken = useRef(invitation.token);
  const [preview, setPreview] = useState<InvitationPreview | null>(null);
  const [result, setResult] = useState<InvitationResult | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(Boolean(invitation.token));
  const [busy, setBusy] = useState(false);
  const [login, setLogin] = useState(false);
  const [refresh, setRefresh] = useState(0);

  useEffect(() => {
    function receiveInvitation() {
      if (!new URLSearchParams(location.hash.slice(1)).has("invite")) return;
      const next = captureOrganizationInvitation();
      currentToken.current = next.token;
      setInvitation(next);
      setPreview(null);
      setResult(null);
      setError("");
      setLogin(false);
      setBusy(false);
      setRefresh((value) => value + 1);
    }
    window.addEventListener("hashchange", receiveInvitation);
    return () => window.removeEventListener("hashchange", receiveInvitation);
  }, []);

  useEffect(() => {
    if (!invitation.token) {
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    setError("");
    void api<InvitationPreview>(
      "/api/v1/invitation-links/preview",
      "POST",
      { token: invitation.token },
      controller.signal,
    )
      .then((data) => {
        if (!controller.signal.aborted) setPreview(data);
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [invitation.token, refresh]);

  async function accept() {
    setBusy(true);
    setError("");
    try {
      const accepted = await api<InvitationResult>("/api/v1/invitation-links/accept", "POST", {
        token: invitation.token,
      });
      if (currentToken.current !== invitation.token) return;
      setResult(accepted);
      clearOrganizationInvitation();
    } catch (cause) {
      if (currentToken.current !== invitation.token) return;
      setError(errorMessage(cause));
      if (cause instanceof APIError && cause.status === 401) setLogin(true);
    } finally {
      if (currentToken.current === invitation.token) setBusy(false);
    }
  }

  async function switchAccount() {
    setBusy(true);
    setError("");
    try {
      await api("/api/auth/logout", "POST");
      window.location.assign("/?page=join");
    } catch (cause) {
      setError(errorMessage(cause));
      setBusy(false);
    }
  }

  if (login && !me) {
    return (
      <>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b bg-card px-6 py-3 text-sm">
          <span>{t("登录后返回组织邀请，再确认是否加入。")}</span>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setLogin(false)}
          >
            {t("返回邀请")}
          </Button>
        </div>
        <Login
          providers={providers}
          development={development}
          error={new URLSearchParams(location.search).get("error") || undefined}
        />
      </>
    );
  }

  return (
    <div className="min-h-screen bg-background">
      <header className="mx-auto flex max-w-5xl items-center justify-between gap-4 p-6">
        <Logo />
        <div className="flex items-center gap-2">
          <ThemeToggle />
          <LanguageSwitch />
        </div>
      </header>
      <main className="mx-auto max-w-md px-4 pb-10 pt-10">
        <Card className="gap-0 shadow-none">
          <CardContent className="p-7">
            <div className="mb-6 flex size-11 items-center justify-center rounded-lg border text-primary">
              <Building2 size={22} />
            </div>
            <p className="mb-2 text-xs uppercase tracking-widest text-muted-foreground">
              {t("组织邀请")}
            </p>
            <h1 className="text-2xl font-semibold tracking-tight">
              {result
                ? result.status === "joined"
                  ? t("已加入组织")
                  : t("你已是组织成员")
                : t("加入组织")}
            </h1>
            {loading ? (
              <div className="mt-6">
                <Loading />
              </div>
            ) : null}
            {invitation.error ? (
              <p
                role="alert"
                className="mt-5 text-sm text-destructive"
              >
                {invitation.error === "storage"
                  ? t("无法在此浏览器保存邀请，请允许当前标签页存储后重新打开链接。")
                  : t("邀请链接缺失或无效，请联系管理员获取新链接。")}
              </p>
            ) : null}
            {preview && !loading ? (
              <>
                <h2 className="mt-3 break-words text-lg font-medium">
                  {preview.organization_name}
                </h2>
                {result?.status !== "already_member" ? (
                  <dl className="my-6 grid gap-3 border-y py-4 text-sm">
                    <div className="flex justify-between gap-4">
                      <dt className="text-muted-foreground">{t("加入后的角色")}</dt>
                      <dd>{roleLabel(preview.role_id)}</dd>
                    </div>
                    <div className="flex justify-between gap-4">
                      <dt className="text-muted-foreground">{t("到期时间")}</dt>
                      <dd>{new Date(preview.expires_at).toLocaleString(locale())}</dd>
                    </div>
                  </dl>
                ) : null}
                {result?.status === "already_member" ? (
                  <p className="mb-5 text-sm text-muted-foreground">
                    {result.can_enter
                      ? t("本次邀请不会修改你当前的角色。")
                      : t("你尚未获授资源角色，请联系组织管理员。")}
                  </p>
                ) : null}
                {!result ? (
                  <>
                    <p className="text-sm text-muted-foreground">
                      {preview.role_id === "member"
                        ? t("可开发、构建、发布和操作组织资源，不能管理组织权限。")
                        : t("可查看资源、源码、日志和存储对象，不能修改或发布。")}
                    </p>
                    {preview.targeted ? (
                      <p className="mt-3 text-sm text-muted-foreground">
                        {t("这是定向邀请，只有指定账号或已验证邮箱可以接受。")}
                      </p>
                    ) : null}
                    {me ? (
                      <div className="my-5 rounded-md border p-3 text-sm">
                        <p className="font-medium">{me.user.name || me.user.user_id}</p>
                        <p className="mt-1 break-all text-xs text-muted-foreground">
                          {me.user.email || me.user.user_id} · {me.user.source_id}
                        </p>
                      </div>
                    ) : null}
                  </>
                ) : null}
              </>
            ) : null}
            {error ? (
              <p
                role="alert"
                className="my-5 break-words text-sm text-destructive"
              >
                {error}
              </p>
            ) : null}
            {!loading && !result && preview ? (
              <Button
                className="mt-5 w-full"
                disabled={busy}
                onClick={() => (me ? void accept() : setLogin(true))}
              >
                {busy ? t("加入中…") : me ? t("加入组织") : t("登录后加入")}
              </Button>
            ) : null}
            {result?.can_enter ? (
              <Button
                className="mt-5 w-full"
                onClick={() =>
                  window.location.assign(
                    "/?workspace=" + encodeURIComponent(result.organization_id),
                  )
                }
              >
                {t("进入组织")}
              </Button>
            ) : null}
            {!loading && !preview && invitation.token ? (
              <Button
                className="mt-5 w-full"
                variant="outline"
                onClick={() => setRefresh((value) => value + 1)}
              >
                {t("重试")}
              </Button>
            ) : null}
            {me && !result && preview ? (
              <Button
                className="mt-3 w-full"
                variant="ghost"
                disabled={busy}
                onClick={() => void switchAccount()}
              >
                {t("切换账号")}
              </Button>
            ) : null}
            {!me && preview && !result ? (
              <p className="mt-4 text-center text-xs text-muted-foreground">
                {t("登录不会自动加入，确认后才会成为组织成员。")}
              </p>
            ) : null}
            <Button
              className="mt-3 w-full"
              variant="ghost"
              onClick={() => {
                clearOrganizationInvitation();
                window.location.assign("/");
              }}
            >
              {t("返回控制台")}
            </Button>
          </CardContent>
        </Card>
        <p className="mt-6 flex items-center justify-center gap-2 text-xs text-muted-foreground">
          <ShieldCheck size={14} />
          {t("独立账号 · 安全会话 · 明确权限")}
        </p>
      </main>
    </div>
  );
}
