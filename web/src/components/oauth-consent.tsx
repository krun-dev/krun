import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCheck,
  ChevronRight,
  Cloud,
  Code2,
  KeyRound,
  Layers3,
  LoaderCircle,
  LogOut,
  ShieldCheck,
  Terminal,
  X,
} from "lucide-react";
import { api, type Me, type Provider } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Logo } from "./common";
import { LanguageSwitch } from "./language";
import { Login } from "./login";

type RequestInfo = {
  application: string;
  client_id: string;
  scopes: string[];
  expires_at: string;
  status: string;
  device_code: string;
  callback: string;
};
export function OAuthConsent({
  me,
  providers,
  development,
  onLogout,
}: {
  me: Me | null;
  providers: Provider[];
  development: boolean;
  onLogout: () => void;
}) {
  useTranslation();
  const request = new URLSearchParams(location.search).get("oauth") || "";
  const [info, setInfo] = useState<RequestInfo | null>(null),
    [failed, setFailed] = useState(""),
    [busy, setBusy] = useState(false),
    [done, setDone] = useState(""),
    [code, setCode] = useState("");
  useEffect(() => {
    if (!request) return;
    const controller = new AbortController();
    api<RequestInfo>(
      "/api/auth/oauth/requests/" + encodeURIComponent(request),
      "GET",
      undefined,
      controller.signal,
    )
      .then((value) => {
        setInfo(value);
        if (value.status !== "pending") setDone(value.status);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setFailed(errorMessage(e));
      });
    return () => controller.abort();
  }, [request]);
  async function decide(allow: boolean) {
    setBusy(true);
    setFailed("");
    try {
      const result = await api<{ redirect_url: string; status: string }>(
        "/api/auth/oauth/requests/" + encodeURIComponent(request),
        "POST",
        { allow },
      );
      setDone(result.status);
      if (result.redirect_url) {
        const target = new URL(result.redirect_url);
        if (
          target.protocol !== "http:" ||
          !["localhost", "127.0.0.1", "[::1]"].includes(target.hostname) ||
          target.pathname !== "/oauth/callback"
        )
          throw new Error(t("授权回调地址无效"));
        window.location.assign(result.redirect_url);
      }
    } catch (e) {
      setFailed(errorMessage(e));
      setBusy(false);
    }
  }
  if (request && !info && !failed)
    return (
      <div className="grid min-h-screen place-items-center bg-background">
        <LoaderCircle
          className="animate-spin text-primary"
          aria-label={t("正在加载授权请求")}
        />
      </div>
    );
  if (request && info && !me && !done)
    return (
      <Login
        providers={providers}
        development={development}
        application={info.application}
        error={new URLSearchParams(location.search).get("error") ?? undefined}
      />
    );
  const app = info?.application || "krun CLI",
    isWrangler = info?.client_id === "krun-wrangler",
    AppIcon = isWrangler ? Cloud : Terminal;
  const permissions = info?.scopes.includes("krun:control")
    ? [
        {
          icon: Layers3,
          title: t("访问你的工作空间"),
          text: t("按你的当前角色管理组织、资源和部署。"),
        },
      ]
    : [
        ...(info?.scopes.includes("account:read")
          ? [
              {
                icon: Layers3,
                title: t("查看工作空间"),
                text: t("读取你有权访问的组织和 Worker。"),
              },
            ]
          : []),
        ...(info?.scopes.some((s) =>
          ["workers:write", "workers_scripts:write"].includes(s),
        )
          ? [
              {
                icon: Code2,
                title: t("管理 Worker 与部署"),
                text: t("上传代码、发布版本，以及管理预览环境、变量和密钥。"),
              },
            ]
          : []),
        ...(info?.scopes.includes("user:read")
          ? [
              {
                icon: ShieldCheck,
                title: t("读取基本账号信息"),
                text: t("用于在终端识别当前登录账号。"),
              },
            ]
          : []),
      ];
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="mx-auto flex max-w-6xl items-center justify-between px-6 py-6 md:px-10">
        <a href="/" aria-label={t("返回控制台")}>
          <Logo />
        </a>
        <LanguageSwitch />
      </header>
      <main className="mx-auto grid max-w-6xl gap-10 px-6 pb-14 pt-5 md:grid-cols-[.9fr_1fr] md:gap-16 md:px-10 md:pt-14">
        <section className="relative hidden flex-col justify-between overflow-hidden rounded-3xl md:flex bg-[#141b18] p-7 text-white md:min-h-[570px] md:p-10">
          <div className="pointer-events-none absolute -right-32 -top-32 size-96 rounded-full border border-emerald-300/10" />
          <div className="pointer-events-none absolute -right-16 -top-16 size-64 rounded-full border border-emerald-300/10" />
          <div className="relative">
            <p className="flex items-center gap-2 text-xs font-medium tracking-widest text-emerald-300">
              <span className="size-1.5 rounded-full bg-emerald-300" />
              {t("开发工具授权")}
            </p>
            <h1 className="mt-7 text-3xl font-semibold leading-tight tracking-tight md:text-4xl">
              {t("让终端，")}
              <br />
              {t("连接你的工作空间。")}
            </h1>
            <p className="mt-5 max-w-xs text-sm leading-7 text-white/55">
              {t("使用同一个 krun 账号，在浏览器与命令行之间顺畅工作。")}
            </p>
            <div className="my-10 flex items-center gap-4">
              <div className="flex size-16 items-center justify-center rounded-2xl border border-white/10 bg-white/5">
                <AppIcon
                  size={27}
                  className={
                    isWrangler ? "text-orange-300" : "text-emerald-200"
                  }
                />
              </div>
              <div className="flex flex-1 items-center text-emerald-300/40">
                <span className="h-px flex-1 bg-current" />
                <ArrowRight size={17} />
                <span className="h-px flex-1 bg-current" />
              </div>
              <div className="grid size-16 place-items-center rounded-2xl border border-emerald-300/25 bg-emerald-300/10">
                <Terminal size={27} className="text-emerald-300" />
              </div>
            </div>
          </div>
          <div className="relative border-t border-white/10 pt-6">
            <div className="flex items-start gap-3">
              <ShieldCheck
                size={18}
                className="mt-1 shrink-0 text-emerald-300"
              />
              <div>
                <p className="text-sm font-medium">{t("权限始终由你掌握")}</p>
                <p className="mt-2 text-xs leading-6 text-white/50">
                  {t(
                    "工具只能执行你本来就有权执行的操作。退出终端登录后，本次授权随即撤销。",
                  )}
                </p>
              </div>
            </div>
          </div>
        </section>
        <section
          className="flex flex-col justify-center"
          aria-label={t("授权确认")}
        >
          {!request ? (
            <>
              <div className="mb-6 grid size-12 place-items-center rounded-2xl border bg-card">
                <KeyRound size={22} className="text-primary" />
              </div>
              <h2 className="text-2xl font-semibold tracking-tight">
                {t("连接你的终端")}
              </h2>
              <p className="mt-3 text-sm leading-6 text-muted-foreground">
                {t("输入终端显示的确认码，继续登录。")}
              </p>
              <form
                className="mt-7 space-y-4"
                onSubmit={(e) => {
                  e.preventDefault();
                  window.location.assign(
                    "/?oauth=" + encodeURIComponent(code.trim().toUpperCase()),
                  );
                }}
              >
                <label className="text-sm font-medium" htmlFor="terminal-code">
                  {t("终端确认码")}
                </label>
                <Input
                  id="terminal-code"
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder="XXXXXXX-XXXXXXX"
                  className="h-12 font-mono tracking-wider"
                  required
                  maxLength={15}
                />
                <Button type="submit" className="h-11 w-full">
                  {t("继续")}
                  <ArrowRight size={16} />
                </Button>
              </form>
            </>
          ) : done ? (
            <div role="status" className="py-8">
              <div
                className={`mb-6 grid size-14 place-items-center rounded-full ${done === "approved" ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground"}`}
              >
                {done === "approved" ? (
                  <CheckCheck size={26} />
                ) : (
                  <X size={26} />
                )}
              </div>
              <h2 className="text-3xl font-semibold tracking-tight">
                {done === "approved" ? t("连接已确认") : t("已取消授权")}
              </h2>
              <p className="mt-4 text-sm leading-7 text-muted-foreground">
                {done === "approved"
                  ? t("返回终端继续工作。现在可以关闭这个页面。")
                  : t("没有向终端授予访问权限。你可以关闭这个页面。")}
              </p>
              <Button variant="outline" className="mt-7" asChild>
                <a href="/">
                  {t("返回控制台")}
                  <ChevronRight size={15} />
                </a>
              </Button>
            </div>
          ) : !info ? (
            <>
              <h2 className="text-2xl font-semibold">
                {t("此授权请求已失效")}
              </h2>
              <p
                role="alert"
                className="mt-4 text-sm leading-7 text-muted-foreground"
              >
                {t("请在终端重新运行登录命令，获取新的授权链接。")}
              </p>
              <Button variant="outline" className="mt-7 w-fit" asChild>
                <a href="/">
                  <ArrowLeft size={15} />
                  {t("返回控制台")}
                </a>
              </Button>
            </>
          ) : (
            <>
              <div className="mb-6 flex items-center gap-3">
                <div
                  className={`grid size-12 place-items-center rounded-2xl border ${isWrangler ? "border-orange-400/20 bg-orange-400/10 text-orange-500" : "border-primary/20 bg-primary/10 text-primary"}`}
                >
                  <AppIcon size={23} />
                </div>
                <div>
                  <p className="text-xs text-muted-foreground">
                    {t("正在连接")}
                  </p>
                  <p className="mt-1 font-semibold">{app}</p>
                </div>
              </div>
              <h2 className="text-3xl font-semibold tracking-tight">
                {t("允许 {{application}} 访问？", { application: app })}
              </h2>
              <p className="mt-3 text-sm leading-7 text-muted-foreground">
                {t("确认下方账号和权限，连接后即可回到终端。")}
              </p>
              <div className="mt-7 flex items-center gap-3 rounded-xl border bg-card p-4">
                <div className="grid size-10 shrink-0 place-items-center rounded-full bg-primary/10 font-semibold text-primary">
                  {(me?.user.name || "K").slice(0, 1).toUpperCase()}
                </div>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-semibold">
                    {me?.user.name}
                  </p>
                  <p className="mt-1 truncate text-xs text-muted-foreground">
                    {me?.user.email || me?.user.user_id}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy}
                  onClick={async () => {
                    try {
                      await api("/api/auth/logout", "POST", {});
                      onLogout();
                    } catch (e) {
                      setFailed(errorMessage(e));
                    }
                  }}
                >
                  <LogOut size={14} />
                  {t("切换账号")}
                </Button>
              </div>
              {info.device_code && (
                <div className="mt-5 rounded-xl border border-primary/20 bg-primary/5 px-4 py-3">
                  <p className="text-xs text-muted-foreground">
                    {t("请与终端上的确认码核对")}
                  </p>
                  <p className="mt-2 font-mono text-xl font-semibold tracking-widest">
                    {info.device_code}
                  </p>
                </div>
              )}
              <div className="mt-7 space-y-5">
                {permissions.map(({ icon: Icon, title, text }) => (
                  <div className="flex gap-3" key={title}>
                    <Icon
                      size={18}
                      className="mt-1 shrink-0 text-muted-foreground"
                    />
                    <div>
                      <p className="text-sm font-medium">{title}</p>
                      <p className="mt-1 text-xs leading-6 text-muted-foreground">
                        {text}
                      </p>
                    </div>
                  </div>
                ))}
                {info.scopes.includes("offline_access") && (
                  <div className="flex gap-3">
                    <KeyRound
                      size={18}
                      className="mt-1 shrink-0 text-muted-foreground"
                    />
                    <div>
                      <p className="text-sm font-medium">{t("保持终端登录")}</p>
                      <p className="mt-1 text-xs leading-6 text-muted-foreground">
                        {t(
                          "无需反复登录；可随时通过终端退出，授权最长保留 30 天。",
                        )}
                      </p>
                    </div>
                  </div>
                )}
              </div>
              {failed && (
                <p
                  role="alert"
                  className="mt-5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 text-sm text-destructive"
                >
                  {failed}
                </p>
              )}
              <div className="mt-8 grid grid-cols-[1fr_1.4fr] gap-3 border-t pt-6">
                <Button
                  variant="outline"
                  className="h-12"
                  disabled={busy}
                  onClick={() => void decide(false)}
                >
                  {t("取消授权")}
                </Button>
                <Button
                  className="h-12"
                  disabled={busy || info.status !== "pending"}
                  onClick={() => void decide(true)}
                >
                  {busy ? (
                    <LoaderCircle size={17} className="animate-spin" />
                  ) : (
                    <Check size={17} />
                  )}{" "}
                  {t("允许连接")}
                </Button>
              </div>
              <p className="mt-4 text-center text-xs leading-6 text-muted-foreground">
                {t("仅在你主动发起终端登录时授权。不确定时，请取消。")}
              </p>
            </>
          )}
        </section>
      </main>
      <footer className="mx-auto max-w-6xl px-6 pb-8 text-center text-xs text-muted-foreground">
        {t("krun · 为团队构建的开发平台")}
      </footer>
    </div>
  );
}
