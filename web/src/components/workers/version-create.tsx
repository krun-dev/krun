import { useRef, useState } from "react";
import * as DropdownMenu from "radix-ui/dropdown-menu";
import { Ellipsis, LoaderCircle, PackagePlus } from "lucide-react";
import { api, APIError } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import { shortID, type Build, type Environment, type Version } from "@/lib/workers";
import { Field } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { VersionConfiguration } from "./version-configuration";

export function BuildVersionMenu({ disabled, create }: { disabled: boolean; create: () => void }) {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={t("更多构建操作")} disabled={disabled}>
          <Ellipsis size={16} />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={4}
          className="z-50 min-w-48 rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
        >
          <DropdownMenu.Item
            onSelect={create}
            className="flex cursor-pointer items-center gap-2 rounded-sm px-3 py-2 text-sm outline-none focus:bg-accent"
          >
            <PackagePlus size={14} />
            {t("创建待部署版本")}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function CreateVersionDialog({
  base,
  build,
  env,
  close,
  created,
}: {
  base: string;
  build: Build;
  env: Environment;
  close: () => void;
  created: (version: Version) => void;
}) {
  const [tag, setTag] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const submitting = useRef(false);
  const attempt = useRef({ input: "", key: "" });

  async function submit() {
    if (submitting.current || stale) {
      return;
    }
    const input = {
      build_id: build.id,
      expected_configuration_revision: env.configuration_revision,
      tag: tag.trim(),
      message: message.trim(),
    };
    const serialized = JSON.stringify(input);
    if (
      new TextEncoder().encode(input.tag).length > 100 ||
      new TextEncoder().encode(input.message).length > 1000
    ) {
      setError(t("版本标签或说明过长，请缩短后重试。"));
      return;
    }
    if (attempt.current.input !== serialized) {
      attempt.current = { input: serialized, key: crypto.randomUUID() };
    }
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const version = await api<Version>(
        base + "/environments/" + encodeURIComponent(env.name) + "/versions",
        "POST",
        {
          ...input,
          idempotency_key: attempt.current.key,
        },
      );
      created(version);
    } catch (cause) {
      if (cause instanceof APIError && cause.code === "REVISION_CONFLICT") {
        setStale(true);
        setError(t("环境配置已改变，请关闭后重新创建并核对配置。"));
      } else {
        setError(errorMessage(cause));
      }
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) close();
      }}
    >
      <DialogContent className="max-h-[85dvh] overflow-y-auto" showCloseButton={!busy}>
        <DialogHeader>
          <DialogTitle>{t("创建待部署版本")}</DialogTitle>
          <DialogDescription>
            {t(
              "固定此构建与下方环境配置，留待稍后部署。创建不会改变线上流量，也不会生成访问地址。",
            )}
          </DialogDescription>
        </DialogHeader>
        <dl className="grid gap-2 rounded-md bg-secondary p-3 text-sm">
          <div className="flex justify-between gap-2">
            <dt>{t("来源构建")}</dt>
            <dd>
              <code title={build.id}>{shortID(build.id)}</code>
            </dd>
          </div>
          <div className="flex justify-between gap-2">
            <dt>{t("目标环境")}</dt>
            <dd className="break-all font-medium">{env.name}</dd>
          </div>
          {build.repository_source ? (
            <div className="break-all text-xs text-muted-foreground">
              {build.repository_source.ref} · {build.repository_source.commit?.slice(0, 12)}
            </div>
          ) : null}
        </dl>
        <VersionConfiguration config={env} />
        <Field label={t("版本标签（可选）")}>
          <Input
            value={tag}
            onChange={(event) => setTag(event.target.value)}
            maxLength={100}
            disabled={busy}
          />
        </Field>
        <Field label={t("版本说明（可选）")}>
          <Textarea
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            maxLength={1000}
            disabled={busy}
          />
        </Field>
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button variant="outline" disabled={busy} onClick={close}>
            {t("取消")}
          </Button>
          <Button disabled={busy || stale} onClick={() => void submit()}>
            {busy ? <LoaderCircle size={14} className="animate-spin" /> : <PackagePlus size={14} />}
            {busy ? t("创建中…") : t("创建待部署版本")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
