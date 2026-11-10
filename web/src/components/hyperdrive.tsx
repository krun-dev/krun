import { useEffect, useState, type FormEvent } from "react";
import { Plus } from "lucide-react";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import { Empty, Field, PageHeader } from "@/components/common";
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

export type HyperdriveConfig = {
  id: string;
  name: string;
  host: string;
  port: number;
  database: string;
  user: string;
};

type FormAction = { type: "create" } | { type: "rotate" | "delete"; config: HyperdriveConfig };

export default function HyperdrivePage({
  base,
  canManage,
  notify,
}: {
  base: string;
  canManage: boolean;
  notify: (message: string) => void;
}) {
  const endpoint = base + "/hyperdrive";
  const [items, setItems] = useState<HyperdriveConfig[] | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [error, setError] = useState("");
  const [action, setAction] = useState<FormAction | null>(null);
  const [formError, setFormError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void api<{ items: HyperdriveConfig[] }>(endpoint, "GET", undefined, controller.signal)
      .then((data) => setItems(data.items))
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [endpoint, refresh]);

  function open(next: FormAction | null) {
    setFormError("");
    setAction(next);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!action) return;
    const values = new FormData(event.currentTarget);
    const password = String(values.get("password") || "");
    const path = action.type === "create" ? endpoint : endpoint + "/" + action.config.id;
    setBusy(true);
    setFormError("");
    try {
      if (action.type === "create") {
        await api(path, "POST", {
          name: values.get("name"),
          origin: {
            scheme: "postgresql",
            host: values.get("host"),
            port: Number(values.get("port")),
            database: values.get("database"),
            user: values.get("user"),
            password,
          },
        });
      } else if (action.type === "rotate") {
        await api(path, "PATCH", { origin: { password } });
      } else {
        await api(path, "DELETE");
      }
      open(null);
      setRefresh((current) => current + 1);
      notify(t("Hyperdrive 配置已更新。"));
    } catch (cause) {
      setFormError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }

  const actionTitle =
    action?.type === "create"
      ? t("创建 Hyperdrive 配置")
      : action?.type === "rotate"
        ? t("轮换源库密码")
        : t("删除 Hyperdrive 配置");

  return (
    <>
      <PageHeader
        eyebrow={t("数据库")}
        title="Hyperdrive"
        description={t("由 krun 保管 PostgreSQL 源库凭据，为 Worker 提供数据库绑定。")}
        action={
          canManage ? (
            <Button onClick={() => open({ type: "create" })}>
              <Plus size={16} />
              {t("创建 Hyperdrive 配置")}
            </Button>
          ) : undefined
        }
      />
      {error ? (
        <p
          role="alert"
          className="text-sm text-destructive"
        >
          {error}
        </p>
      ) : items === null ? (
        <p
          role="status"
          className="text-sm text-muted-foreground"
        >
          {t("加载中…")}
        </p>
      ) : items.length === 0 ? (
        <Empty
          title={t("暂无 Hyperdrive 配置")}
          description={t("创建后可将数据库绑定到 Worker。")}
        />
      ) : (
        <div className="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("名称")}</TableHead>
                <TableHead>{t("源数据库")}</TableHead>
                <TableHead>{t("配置 ID")}</TableHead>
                {canManage && <TableHead>{t("操作")}</TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={item.id}>
                  <TableCell className="font-medium">{item.name}</TableCell>
                  <TableCell>
                    {item.host}:{item.port}/{item.database}
                  </TableCell>
                  <TableCell>
                    <code>{item.id}</code>
                  </TableCell>
                  {canManage && (
                    <TableCell>
                      <div className="flex gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => open({ type: "rotate", config: item })}
                        >
                          {t("轮换源库密码")}
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => open({ type: "delete", config: item })}
                        >
                          {t("删除")}
                        </Button>
                      </div>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      <Dialog
        open={Boolean(action)}
        onOpenChange={(shown) => {
          if (!shown && !busy) open(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{actionTitle}</DialogTitle>
            <DialogDescription>
              {action?.type === "delete"
                ? t("删除后，使用此配置的 Worker 无法再建立新的数据库连接。")
                : t("源库密码由平台保管，Worker 只获得代理接入凭据。")}
            </DialogDescription>
          </DialogHeader>
          {action && (
            <form
              className="grid gap-3"
              onSubmit={(event) => void submit(event)}
            >
              {action.type === "create" && (
                <>
                  <Field label={t("名称")}>
                    <Input
                      name="name"
                      required
                      maxLength={100}
                    />
                  </Field>
                  <Field label={t("源库域名")}>
                    <Input
                      name="host"
                      required
                    />
                  </Field>
                  <Field label={t("端口")}>
                    <Input
                      name="port"
                      type="number"
                      defaultValue={5432}
                      min={1}
                      max={65535}
                      required
                    />
                  </Field>
                  <Field label={t("库名")}>
                    <Input
                      name="database"
                      required
                    />
                  </Field>
                  <Field label={t("源库用户名")}>
                    <Input
                      name="user"
                      required
                    />
                  </Field>
                </>
              )}
              {action.type !== "delete" && (
                <Field label={t("源库密码")}>
                  <Input
                    name="password"
                    type="password"
                    autoComplete="new-password"
                    required
                  />
                </Field>
              )}
              {formError && (
                <p
                  role="alert"
                  className="text-sm text-destructive"
                >
                  {formError}
                </p>
              )}
              <div className="flex justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => open(null)}
                >
                  {t("取消")}
                </Button>
                <Button
                  type="submit"
                  disabled={busy}
                  variant={action.type === "delete" ? "destructive" : "default"}
                >
                  {action.type === "delete" ? t("删除") : t("保存")}
                </Button>
              </div>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
