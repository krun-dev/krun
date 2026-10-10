import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowRight, Mail } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";

type Challenge = { challenge_id: string; expires_in: number; retry_after: number };

export function EmailLogin({ provider, busy, onBusy }: {
  provider: string;
  busy: boolean;
  onBusy: (value: boolean) => void;
}) {
  useTranslation();
  const id = useId();
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [retryAt, setRetryAt] = useState(0);
  const [remaining, setRemaining] = useState(0);
  const [failed, setFailed] = useState("");
  const endpoint = `/auth/${encodeURIComponent(provider)}/email`;

  useEffect(() => {
    if (!retryAt) return;
    const update = () => setRemaining(Math.max(0, Math.ceil((retryAt - Date.now()) / 1000)));
    update();
    const timer = window.setInterval(update, 1000);
    return () => window.clearInterval(timer);
  }, [retryAt]);

  async function sendCode() {
    onBusy(true);
    setFailed("");
    try {
      const returnTo = `/console/${window.location.search.includes("cli=") ? window.location.search : ""}`;
      const result = await api<Challenge>(`${endpoint}/code`, "POST", { email, return_to: returnTo });
      setChallenge(result);
      setCode("");
      setRetryAt(Date.now() + result.retry_after * 1000);
    } catch (error) {
      setFailed(errorMessage(error));
    } finally {
      onBusy(false);
    }
  }

  async function verify() {
    if (!challenge) return;
    onBusy(true);
    setFailed("");
    try {
      const result = await api<{ redirect_url: string }>(`${endpoint}/verify`, "POST", { challenge_id: challenge.challenge_id, code });
      window.location.assign(result.redirect_url);
    } catch (error) {
      setFailed(errorMessage(error));
      onBusy(false);
    }
  }

  return (
    <form className="grid gap-4 rounded-lg border bg-card p-4" onSubmit={(event) => {
      event.preventDefault();
      if (!busy) void (challenge ? verify() : sendCode());
    }}>
      <div className="flex items-center gap-2 text-sm font-medium">
        <Mail size={17} className="text-primary" />
        {t("邮箱登录")}
      </div>
      <div className="grid gap-2">
        <Label htmlFor={`${id}-email`}>{t("邮箱地址")}</Label>
        <Input id={`${id}-email`} type="email" autoComplete="email" required maxLength={254}
          value={email} disabled={busy || !!challenge} onChange={(event) => setEmail(event.target.value)}
          placeholder="you@example.com" />
      </div>
      {challenge && (
        <>
          <p role="status" className="text-xs leading-relaxed text-muted-foreground">
            {t("验证码已发送至 {{email}}，10 分钟内有效。", { email })}
          </p>
          <div className="grid gap-2">
            <Label htmlFor={`${id}-code`}>{t("邮箱验证码")}</Label>
            <Input id={`${id}-code`} inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}"
              required maxLength={6} value={code} disabled={busy} autoFocus
              onChange={(event) => setCode(event.target.value.replace(/\D/g, "").slice(0, 6))}
              placeholder={t("输入 6 位验证码")} />
          </div>
        </>
      )}
      {failed && <p role="alert" className="text-sm text-destructive">{t(failed)}</p>}
      <Button type="submit" disabled={busy} className="w-full justify-between">
        {busy ? t("请稍候…") : challenge ? t("验证并登录") : t("发送验证码")}
        <ArrowRight size={16} />
      </Button>
      {challenge ? (
        <div className="flex items-center justify-between">
          <Button type="button" variant="ghost" size="sm" disabled={busy || remaining > 0} onClick={() => void sendCode()}>
            {remaining > 0 ? t("{{seconds}} 秒后重新发送", { seconds: remaining }) : t("重新发送")}
          </Button>
          <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => {
            setChallenge(null);
            setCode("");
            setFailed("");
          }}>{t("更换邮箱")}</Button>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("无需密码，验证邮箱即可登录。")}</p>
      )}
    </form>
  );
}
