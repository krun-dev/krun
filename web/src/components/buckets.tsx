import { useCallback, useEffect, useState } from "react";
import { Database, Plus } from "lucide-react";
import { api, type List } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import type { WorkerBucket } from "@/lib/object-storage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
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
import { Empty, Field, Loading, PageHeader } from "@/components/common";
import { BucketObjects } from "@/components/bucket-objects";

export function BucketsPage({
  base,
  system,
  notify,
}: {
  base: string;
  system: boolean;
  notify: (message: string) => void;
}) {
  const [buckets, setBuckets] = useState<WorkerBucket[] | null>(null);
  const [error, setError] = useState("");
  const [formError, setFormError] = useState("");
  const [name, setName] = useState("");
  const [create, setCreate] = useState(false);
  const [target, setTarget] = useState<WorkerBucket | null>(null);
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [selected, setSelected] = useState(
    () => new URLSearchParams(location.search).get("bucket") || "",
  );
  function selectBucket(id: string) {
    setSelected(id);
    const url = new URL(location.href);
    url.searchParams.delete("prefix");
    if (id) {
      url.searchParams.set("bucket", id);
    } else {
      url.searchParams.delete("bucket");
    }
    history.replaceState(null, "", url);
  }
  const reload = useCallback(() => setRefresh((value) => value + 1), []);
  useEffect(() => {
    if (system) {
      return;
    }
    const controller = new AbortController();
    setError("");
    void api<List<WorkerBucket>>(base + "/workers/buckets", "GET", undefined, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) {
          setBuckets(result.items);
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(cause));
        }
      });
    return () => controller.abort();
  }, [base, system, refresh]);
  async function createBucket() {
    setBusy(true);
    setFormError("");
    try {
      await api(base + "/workers/buckets", "POST", { name });
      setCreate(false);
      setName("");
      reload();
      notify(t("桶已创建，可在 Worker 环境中添加 R2 绑定。"));
    } catch (cause) {
      setFormError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }
  async function toggleBucket() {
    if (!target) {
      return;
    }
    setBusy(true);
    try {
      await api(base + "/workers/buckets/" + encodeURIComponent(target.id), "PATCH", {
        enabled: !target.enabled,
        revision: target.revision,
      });
      notify(target.enabled ? t("桶已停用。") : t("桶已启用。"));
    } catch (cause) {
      notify(errorMessage(cause));
    } finally {
      setBusy(false);
      setTarget(null);
      reload();
    }
  }
  if (system) {
    return (
      <Empty title={t("请选择组织")} description={t("对象存储按组织管理，请先进入一个组织。")} />
    );
  }
  const opened = buckets?.find((bucket) => bucket.id === selected);
  if (opened?.enabled) {
    return (
      <BucketObjects
        key={base + opened.id}
        base={base}
        bucket={opened}
        back={() => selectBucket("")}
        notify={notify}
      />
    );
  }
  return (
    <>
      <PageHeader
        eyebrow={t("存储服务")}
        title={t("对象存储")}
        description={t("管理逻辑桶，并通过兼容 Cloudflare R2 的 binding 在 Worker 中使用。")}
        action={
          <Button
            onClick={() => {
              setFormError("");
              setCreate(true);
            }}
          >
            <Plus size={16} />
            {t("创建桶")}
          </Button>
        }
      />
      {error ? (
        <div role="alert" className="mb-4 flex items-center gap-3 text-sm text-destructive">
          {error}
          <Button variant="outline" size="sm" onClick={reload}>
            {t("重试")}
          </Button>
        </div>
      ) : null}
      {buckets === null && !error ? (
        <Loading />
      ) : buckets?.length ? (
        <Card className="py-0 shadow-none">
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("桶名称")}</TableHead>
                  <TableHead>{t("状态")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("创建时间")}</TableHead>
                  <TableHead>{t("操作")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {buckets.map((bucket) => (
                  <TableRow key={bucket.id}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <Database size={16} className="text-muted-foreground" />
                        <Button
                          variant="link"
                          className="h-auto p-0 font-medium"
                          disabled={!bucket.enabled}
                          onClick={() => selectBucket(bucket.id)}
                        >
                          {bucket.name}
                        </Button>
                      </div>
                      <code className="text-xs text-muted-foreground">{bucket.id}</code>
                    </TableCell>
                    <TableCell>
                      <Badge variant={bucket.enabled ? "default" : "secondary"}>
                        {bucket.enabled ? t("已启用") : t("已停用")}
                      </Badge>
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {new Date(bucket.created_at).toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={busy}
                        onClick={() => setTarget(bucket)}
                      >
                        {bucket.enabled ? t("停用") : t("启用")}
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : !error ? (
        <Empty
          title={t("还没有桶")}
          description={t("创建一个桶，然后在 Worker 的环境配置中添加绑定。")}
        />
      ) : null}
      <Dialog
        open={create}
        onOpenChange={(open) => {
          if (!busy) {
            setCreate(open);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("创建桶")}</DialogTitle>
            <DialogDescription>
              {t("桶在本组织内共享。名称为 3–63 位小写字母、数字或连字符，首尾须为字母或数字。")}
            </DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              void createBucket();
            }}
          >
            <Field label={t("桶名称")}>
              <Input
                autoFocus
                aria-label={t("桶名称")}
                value={name}
                placeholder="product-images"
                disabled={busy}
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            {formError ? (
              <p role="alert" className="text-sm text-destructive">
                {formError}
              </p>
            ) : null}
            <Button
              type="submit"
              disabled={busy || !/^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/.test(name)}
            >
              {busy ? t("保存中…") : t("创建桶")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={Boolean(target)}
        onOpenChange={(open) => {
          if (!busy && !open) {
            setTarget(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{target?.enabled ? t("停用桶") : t("启用桶")}</DialogTitle>
            <DialogDescription>
              {target?.enabled
                ? t("停用会影响使用此桶的所有 Worker，已有数据保留。")
                : t("启用后，Worker 可以重新绑定和访问此桶。")}
            </DialogDescription>
          </DialogHeader>
          <p className="break-all font-mono text-sm">{target?.name}</p>
          <Button disabled={busy} onClick={() => void toggleBucket()}>
            {t("确认")}
          </Button>
        </DialogContent>
      </Dialog>
    </>
  );
}
