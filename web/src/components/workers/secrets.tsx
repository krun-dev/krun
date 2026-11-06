import { useId, useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { t } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Field } from "@/components/common";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export function SecretsEditor({
  names,
  disabled,
  previewBase = false,
  published = false,
  effectDescription,
  pendingNames = [],
  saveLabel,
  onSave,
  onDelete,
}: {
  names: string[];
  disabled: boolean;
  previewBase?: boolean;
  published?: boolean;
  effectDescription?: string;
  pendingNames?: string[];
  saveLabel?: string;
  onSave: (name: string, value: string) => Promise<boolean>;
  onDelete: (name: string) => Promise<boolean>;
}) {
  const valueID = useId();
  const [editing, setEditing] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [visible, setVisible] = useState(false);
  const [deleting, setDeleting] = useState<string>();
  const validName = /^[A-Za-z_$][A-Za-z0-9_$]{0,127}$/.test(name) && !name.startsWith("KRUN_");
  const duplicate = editing === "" && names.includes(name);
  const effect =
    effectDescription ??
    (previewBase
      ? t("仅影响之后新建的 Preview，已有 Preview 和生产环境保持不变。")
      : published
        ? t("保存或删除后会自动发布新版本，无需重新构建代码。")
        : t("保存后在首次部署时生效。"));

  function closeEditor() {
    setEditing(null);
    setName("");
    setValue("");
    setVisible(false);
  }

  function openEditor(secretName: string) {
    setEditing(secretName);
    setName(secretName);
    setValue("");
    setVisible(false);
  }

  return (
    <section
      aria-label={t("Secrets")}
      className="grid gap-3 border-t pt-4"
    >
      <div>
        <h3 className="text-sm font-medium">{t("Secrets")}</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          {t(
            "用于 API Token、密码等敏感信息，代码通过 env.名称 读取。值加密存储，保存后不可查看；可以替换新值或删除。",
          )}
        </p>
        <p className="mt-1 text-xs text-muted-foreground">{effect}</p>
      </div>
      {names.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("尚未配置 Secret。")}</p>
      ) : (
        <ul className="grid gap-2">
          {[...names].sort().map((secretName) => (
            <li
              key={secretName}
              className="flex flex-wrap items-center justify-between gap-3 rounded-md border px-3 py-2"
            >
              <div className="min-w-0">
                <code className="break-all text-xs">{secretName}</code>
                <span className="ml-3 text-xs text-muted-foreground">
                  {pendingNames.includes(secretName) ? t("待保存") : t("已加密 · 不可回显")}
                </span>
              </div>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={disabled}
                  aria-label={t("替换 Secret {{name}}", { name: secretName })}
                  onClick={() => openEditor(secretName)}
                >
                  {t("替换")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={disabled}
                  aria-label={t("删除 Secret {{name}}", { name: secretName })}
                  onClick={() => setDeleting(secretName)}
                >
                  {t("删除")}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      {editing === null ? (
        <div>
          <Button
            variant="outline"
            disabled={disabled || names.length >= 100}
            onClick={() => openEditor("")}
          >
            {t("添加 Secret")}
          </Button>
        </div>
      ) : (
        <form
          className="grid gap-3 rounded-md border p-4"
          onSubmit={async (event) => {
            event.preventDefault();
            if (disabled || !validName || duplicate) return;
            if (await onSave(name, value)) closeEditor();
          }}
        >
          <h4 className="text-sm font-medium">
            {editing ? t("替换 Secret {{name}}", { name: editing }) : t("添加 Secret")}
          </h4>
          <Field label={t("Secret 名称")}>
            <Input
              autoComplete="off"
              placeholder="API_TOKEN"
              value={name}
              disabled={disabled || editing !== ""}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          {duplicate ? (
            <p
              role="alert"
              className="text-xs text-destructive"
            >
              {t("此名称已存在，请使用该行的“替换”按钮。")}
            </p>
          ) : null}
          <div className="grid gap-2">
            <Label htmlFor={valueID}>{editing ? t("新的 Secret 值") : t("Secret 值")}</Label>
            <div className="flex gap-2">
              <Input
                id={valueID}
                type={visible ? "text" : "password"}
                autoComplete="new-password"
                spellCheck={false}
                value={value}
                disabled={disabled}
                onChange={(event) => setValue(event.target.value)}
              />
              <Button
                type="button"
                size="icon"
                variant="outline"
                disabled={disabled}
                aria-label={visible ? t("隐藏输入值") : t("显示输入值")}
                aria-pressed={visible}
                onClick={() => setVisible(!visible)}
              >
                {visible ? <EyeOff size={16} /> : <Eye size={16} />}
              </Button>
            </div>
            {editing ? (
              <p className="text-xs text-muted-foreground">
                {t("旧值不会回填。请输入新值，保存后替换旧值。")}
              </p>
            ) : null}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              type="submit"
              disabled={disabled || !validName || duplicate}
            >
              {saveLabel || (published ? t("保存并发布 Secret") : t("保存 Secret"))}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={disabled}
              onClick={closeEditor}
            >
              {t("取消")}
            </Button>
          </div>
        </form>
      )}
      <Dialog
        open={deleting !== undefined}
        onOpenChange={(open) => {
          if (!open && !disabled) setDeleting(undefined);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("删除 Secret {{name}}", { name: deleting || "" })}</DialogTitle>
            <DialogDescription>{effect}</DialogDescription>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            {t("删除后，代码将无法从此配置读取该 Secret。")}
          </p>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={disabled}
              onClick={() => setDeleting(undefined)}
            >
              {t("取消")}
            </Button>
            <Button
              variant="destructive"
              disabled={disabled}
              onClick={async () => {
                if (deleting && (await onDelete(deleting))) {
                  if (editing === deleting) closeEditor();
                  setDeleting(undefined);
                }
              }}
            >
              {t("确认删除")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
