import { LanguageSwitch } from "./language";
import { EmailLogin } from "./email-login";
import { errorMessage, t, locale, authError } from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  ArrowRight,
  Github,
  Layers3,
  LockKeyhole,
  ShieldCheck,
  Terminal,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Logo } from "./common";
import type { Provider } from "@/lib/api";

export function Login({
  providers,
  development,
  error,
}: {
  providers: Provider[];
  development: boolean;
  error?: string;
}) {
  useTranslation();
  const [failed, setFailed] = useState("");
  const [busy, setBusy] = useState(false);
  async function begin(provider: string) {
    setBusy(true);
    setFailed("");
    try {
      const response = await fetch("/api/auth/csrf", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Accept-Language": locale() },
      });
      if (!response.ok) throw new Error(t("无法获取登录令牌"));
      const { csrf_token } = await response.json();
      const returnTo = `/${window.location.search}`;
      const login = await fetch(`/api/auth/${encodeURIComponent(provider)}/login`, {
        method: "POST",
        credentials: "same-origin",
        headers: {
          Accept: "application/json",
          "Accept-Language": locale(),
          "Content-Type": "application/x-www-form-urlencoded",
          "X-CSRF-Token": csrf_token,
        },
        body: new URLSearchParams({ return_to: returnTo }),
      });
      const data = await login.json();
      if (!login.ok) throw new Error(data.error?.message ?? t("登录失败"));
      window.location.assign(data.redirect_url);
    } catch (error) {
      setFailed(errorMessage(error));
      setBusy(false);
    }
  }
  return (
    <div className="grid min-h-screen lg:grid-cols-[1.05fr_1fr]">
      <div className="relative hidden flex-col overflow-hidden bg-[#151515] p-14 text-white lg:flex">
        <Logo dark />
        <div className="absolute right-[-110px] top-[18%] size-[500px] rounded-full border border-white/5" />
        <div className="absolute right-[-45px] top-[24%] size-[370px] rounded-full border border-white/5" />
        <div className="relative my-auto max-w-lg">
          <Badge
            className="mb-7 border-white/15 bg-card/5 text-white/65"
            variant="outline"
          >
            {t("YOUR INFRASTRUCTURE, ONE CONTROL PLANE")}
          </Badge>
          <h1 className="text-5xl font-semibold leading-[1.15] tracking-tight">
            {t("构建、运行，")}
            <br />
            {t("尽在掌握。")}
          </h1>
          <p className="mt-6 max-w-sm text-base leading-relaxed text-slate-400">
            {t("让团队在同一个组织协作。")}
            <br />
            {t("从身份认证到资源授权，每一次访问都有明确的边界。")}
          </p>
          <div className="mt-12 grid grid-cols-3 gap-6 border-t border-white/10 pt-7">
            {[
              [ShieldCheck, t("统一身份")],
              [Layers3, t("组织隔离")],
              [Terminal, t("CLI 协作")],
            ].map(([Icon, text]) => {
              const I = Icon as typeof ShieldCheck;
              return (
                <div key={String(text)}>
                  <I size={20} className="mb-3 text-emerald-300" />
                  <p className="text-sm text-slate-300">{String(text)}</p>
                </div>
              );
            })}
          </div>
        </div>
        <p className="text-xs text-slate-500">
          {t("krun · Infrastructure control plane")}
        </p>
      </div>
      <main className="flex items-center justify-center p-6 md:p-12">
        <div className="w-full max-w-sm">
          <div className="mb-6 flex justify-end">
            <LanguageSwitch />
          </div>
          <div className="mb-12 lg:hidden">
            <Logo />
          </div>
          <div className="mb-7 flex size-11 items-center justify-center rounded-lg border bg-card text-primary">
            <LockKeyhole size={22} />
          </div>
          <p className="mb-2 text-xs font-medium uppercase tracking-[0.18em] text-muted-foreground">
            {t("Welcome to krun")}
          </p>
          <h2 className="text-3xl font-semibold tracking-tight">
            {t("登录组织")}
          </h2>
          <p className="mt-3 text-sm leading-relaxed text-muted-foreground">
            {t("使用团队的身份来源继续。首次登录后，")}
            <br />
            {t("由管理员为你分配组织与资源权限。")}
          </p>
          {(error || failed) && (
            <p
              role="alert"
              className="mt-6 rounded-md border border-destructive/25 bg-destructive/10 p-3 text-sm text-destructive"
            >
              {error
                ? t("登录失败：{{v0}}", { v0: authError(error) })
                : t(failed)}
            </p>
          )}
          <div className="mt-8 grid gap-3">
            {providers.map((p) => p.type === "email" ? (
              <EmailLogin key={p.id} provider={p.id} busy={busy} onBusy={setBusy} />
            ) : (
              <form
                key={p.id}
                method="post"
                action={`/api/auth/${encodeURIComponent(p.id)}/login`}
                onSubmit={(e) => {
                  e.preventDefault();
                  void begin(p.id);
                }}
              >
                <input
                  type="hidden"
                  name="return_to"
                  value={`/${window.location.search}`}
                />
                <Button
                  type="submit"
                  variant="outline"
                  className="h-12 w-full justify-between bg-card font-medium"
                  disabled={busy}
                >
                  <span className="flex items-center gap-3">
                    {p.type === "github" ? (
                      <Github size={18} />
                    ) : (
                      <ShieldCheck size={18} />
                    )}
                    {t("通过 {{name}} 登录", {
                      name:
                        p.type === "dev"
                          ? t("本地开发登录")
                          : p.name === "阿里云 IDaaS"
                            ? t("阿里云 IDaaS")
                            : p.name,
                    })}
                  </span>
                  <ArrowRight size={16} className="text-muted-foreground" />
                </Button>
              </form>
            ))}
          </div>
          {providers.length === 0 && (
            <p className="mt-6 text-sm text-muted-foreground">
              {t("管理员尚未配置登录来源。")}
            </p>
          )}
          {development && providers.some((p) => p.type === "dev") && (
            <Card className="mt-6 gap-0 border-primary/20 bg-primary/5 py-0 shadow-none">
              <CardContent className="p-4 text-xs leading-relaxed text-muted-foreground">
                {t(
                  "当前为本地开发环境。开发身份仅供联调，生产配置会拒绝启用此登录方式。",
                )}
              </CardContent>
            </Card>
          )}
          <div className="mt-10 flex items-center gap-2 border-t pt-5 text-xs text-muted-foreground">
            <ShieldCheck size={14} />
            {t("独立账号 · 安全会话 · 最小权限")}
          </div>
        </div>
      </main>
    </div>
  );
}
