import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Copy, Link, UserPlus } from "lucide-react";
import { api } from "@/lib/api";
import { errorMessage, locale, roleLabel, t } from "@/lib/i18n";
import type { OrganizationInvitation } from "@/lib/organization-invitations";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Empty, Field, Loading, SelectField } from "./common";

type InvitationProps = {
  base: string;
  notify: (message: string) => void;
  reload: () => void;
};

export function OrganizationInvitationDialog({ base, notify, reload }: InvitationProps) {
  useTranslation();
  const [open, setOpen] = useState(false);
  const [role, setRole] = useState("member");
  const [maxUses, setMaxUses] = useState("1");
  const [recipientKind, setRecipientKind] = useState("any");
  const [recipient, setRecipient] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<{
    invitation: OrganizationInvitation;
    invite_url: string;
  } | null>(null);
  const urlInput = useRef<HTMLInputElement>(null);

  function changeOpen(value: boolean) {
    if (busy) return;
    setOpen(value);
    setCreated(null);
    setError("");
    if (value) {
      setRole("member");
      setMaxUses("1");
      setRecipientKind("any");
      setRecipient("");
    }
  }

  async function create() {
    setBusy(true);
    setError("");
    try {
      const response = await api<{ invitation: OrganizationInvitation; invite_url: string }>(
        base + "/invitation-links",
        "POST",
        {
          role_id: role,
          max_uses: Number(maxUses),
          ...(recipientKind === "email" ? { target_email: recipient } : {}),
          ...(recipientKind === "user" ? { target_user_id: recipient } : {}),
        },
      );
      setCreated(response);
      reload();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }

  async function copy() {
    if (!created) return;
    try {
      await navigator.clipboard.writeText(created.invite_url);
      notify(t("邀请链接已复制。"));
    } catch {
      urlInput.current?.focus();
      urlInput.current?.select();
      notify(t("已选中邀请链接，请使用系统复制快捷键。"));
    }
  }

  return (
    <>
      <Button onClick={() => changeOpen(true)}>
        <UserPlus size={16} />
        {t("邀请成员")}
      </Button>
      <Dialog open={open} onOpenChange={changeOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{created ? t("邀请链接已生成") : t("邀请成员")}</DialogTitle>
            <DialogDescription>
              {created
                ? t("请现在复制并保存，关闭后无法再次查看完整链接。")
                : t("分享链接，让同事登录并确认加入组织。")}
            </DialogDescription>
          </DialogHeader>
          {created ? (
            <div className="grid gap-4">
              <p className="text-sm">
                {roleLabel(created.invitation.role_id)} · {t("7 天有效")} ·{" "}
                {t("最多 {{count}} 人加入", { count: created.invitation.max_uses })}
              </p>
              <Field label={t("邀请链接")}>
                <Input ref={urlInput} readOnly value={created.invite_url} />
              </Field>
              <p className="text-xs leading-relaxed text-muted-foreground">
                {t("请仅分享给受邀者。撤销链接不会移除已经加入的成员。")}
              </p>
              <Button onClick={() => void copy()}>
                <Copy size={16} />
                {t("复制链接")}
              </Button>
            </div>
          ) : (
            <form
              className="grid gap-5"
              onSubmit={(event) => {
                event.preventDefault();
                void create();
              }}
            >
              <fieldset className="grid gap-3" disabled={busy}>
                <legend className="mb-3 text-sm font-medium">{t("加入后的角色")}</legend>
                {(["member", "viewer"] as const).map((value) => (
                  <label
                    key={value}
                    className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[:checked]:border-primary has-[:checked]:bg-primary/5"
                  >
                    <input
                      type="radio"
                      className="mt-1 accent-primary"
                      name="invitation-role"
                      value={value}
                      checked={role === value}
                      onChange={() => setRole(value)}
                    />
                    <span>
                      <span className="text-sm font-medium">{roleLabel(value)}</span>
                      <span className="mt-1 block text-xs leading-relaxed text-muted-foreground">
                        {value === "member"
                          ? t("可开发、构建、发布和操作组织资源，不能管理组织权限。")
                          : t("可查看资源、源码、日志和存储对象，不能修改或发布。")}
                      </span>
                    </span>
                  </label>
                ))}
              </fieldset>
              <details className="rounded-md border p-3">
                <summary className="cursor-pointer text-sm font-medium">{t("更多设置")}</summary>
                <div className="mt-4 grid gap-4">
                  <Field
                    label={t("最多加入人数")}
                    hint={t("仅统计成功加入的新成员，重复点击和已有成员不占名额。")}
                  >
                    <Input
                      type="number"
                      min={1}
                      max={100}
                      step={1}
                      required
                      value={maxUses}
                      disabled={busy}
                      onChange={(event) => setMaxUses(event.target.value)}
                    />
                  </Field>
                  <Field label={t("邀请对象")}>
                    <SelectField
                      value={recipientKind}
                      disabled={busy}
                      onChange={(value) => {
                        setRecipientKind(value);
                        setRecipient("");
                      }}
                    >
                      <option value="any">{t("任何持有链接的人")}</option>
                      <option value="email">{t("指定已验证邮箱")}</option>
                      <option value="user">{t("指定用户 ID")}</option>
                    </SelectField>
                  </Field>
                  {recipientKind !== "any" ? (
                    <Field
                      label={recipientKind === "email" ? t("受邀邮箱") : t("受邀用户 ID")}
                      hint={
                        recipientKind === "email"
                          ? t("仅接受邮箱验证码登录或 OIDC 已验证的邮箱，普通资料邮箱不作为凭据。")
                          : t("使用平台生成的准确用户 ID，不能填写显示名。")
                      }
                    >
                      <Input
                        type={recipientKind === "email" ? "email" : "text"}
                        required
                        value={recipient}
                        maxLength={recipientKind === "email" ? 254 : 128}
                        disabled={busy}
                        placeholder={recipientKind === "email" ? "teammate@example.com" : "usr_…"}
                        onChange={(event) => setRecipient(event.target.value)}
                      />
                    </Field>
                  ) : null}
                </div>
              </details>
              <p className="flex items-start gap-2 text-xs leading-relaxed text-muted-foreground">
                <Link size={15} className="shrink-0" />
                {t("7 天有效，最多 {{count}} 人加入。管理员身份需加入后单独授予。", {
                  count: Number(maxUses) || 1,
                })}
              </p>
              {error ? (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              ) : null}
              <Button type="submit" disabled={busy}>
                {busy ? t("生成中…") : t("生成邀请链接")}
              </Button>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

export function OrganizationInvitationList({
  base,
  notify,
  reload,
  refresh,
  canManage,
}: InvitationProps & { refresh: number; canManage: boolean }) {
  useTranslation();
  const [page, setPage] = useState<{ items: OrganizationInvitation[]; next_cursor: string } | null>(
    null,
  );
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState("");
  const [target, setTarget] = useState<OrganizationInvitation | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setCursor("");
  }, [base, refresh]);
  useEffect(() => {
    const controller = new AbortController();
    setPage(null);
    setError("");
    void api<{ items: OrganizationInvitation[]; next_cursor: string }>(
      base + "/invitation-links?" + new URLSearchParams({ cursor, limit: "20" }),
      "GET",
      undefined,
      controller.signal,
    )
      .then((data) => {
        if (!controller.signal.aborted) setPage(data);
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [base, refresh, cursor]);
  async function revoke() {
    if (!target || !canManage) return;
    setBusy(true);
    try {
      await api(base + "/invitation-links/" + encodeURIComponent(target.id), "DELETE");
      setTarget(null);
      notify(t("邀请链接已撤销，已加入的成员与角色不变。"));
      reload();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }
  const status = (value: OrganizationInvitation["status"]) =>
    ({ active: t("有效"), expired: t("已过期"), revoked: t("已撤销"), exhausted: t("已满额") })[
      value
    ];
  return (
    <>
      {error ? (
        <p role="alert" className="mb-4 text-sm text-destructive">
          {error}
        </p>
      ) : null}
      {!page && !error ? (
        <Loading />
      ) : page?.items.length ? (
        <div className="rounded-lg border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("加入后的角色")}</TableHead>
                <TableHead>{t("邀请对象")}</TableHead>
                <TableHead>{t("已加入 / 上限")}</TableHead>
                <TableHead>{t("到期时间")}</TableHead>
                <TableHead>{t("状态")}</TableHead>
                <TableHead>{t("操作")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {page.items.map((item) => (
                <TableRow key={item.id}>
                  <TableCell>
                    <p>{roleLabel(item.role_id)}</p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {new Date(item.created_at).toLocaleString(locale())}
                    </p>
                  </TableCell>
                  <TableCell>
                    <span className="break-all">
                      {item.target_email || item.target_user_id || t("任何持有链接的人")}
                    </span>
                  </TableCell>
                  <TableCell>
                    {item.use_count} / {item.max_uses}
                  </TableCell>
                  <TableCell>{new Date(item.expires_at).toLocaleString(locale())}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{status(item.status)}</Badge>
                  </TableCell>
                  <TableCell>
                    {canManage && !item.revoked_at ? (
                      <Button variant="outline" size="sm" onClick={() => setTarget(item)}>
                        {t("撤销")}
                      </Button>
                    ) : null}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : !error ? (
        <Empty title={t("暂无邀请链接")} description={t("点击邀请成员，生成链接并分享给同事。")} />
      ) : null}
      <div className="mt-4 flex justify-end gap-2">
        {cursor ? (
          <Button variant="outline" onClick={() => setCursor("")}>
            {t("返回最新")}
          </Button>
        ) : null}
        {page?.next_cursor ? (
          <Button variant="outline" onClick={() => setCursor(page.next_cursor)}>
            {t("下一页")}
          </Button>
        ) : null}
      </div>
      <Dialog
        open={Boolean(target)}
        onOpenChange={(value) => {
          if (!value && !busy) setTarget(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("撤销此邀请链接？")}</DialogTitle>
            <DialogDescription>
              {t("撤销后，不能再通过此链接加入组织。已加入的成员与角色不受影响。")}
            </DialogDescription>
          </DialogHeader>
          <Button variant="destructive" disabled={busy || !canManage} onClick={() => void revoke()}>
            {t("确认撤销")}
          </Button>
        </DialogContent>
      </Dialog>
    </>
  );
}
