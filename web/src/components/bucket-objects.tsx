import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowLeft, Download, File, Folder, RefreshCw, Upload } from "lucide-react";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import {
  uploadObject,
  type BucketObject,
  type ObjectAccess,
  type ObjectPage,
  type WorkerBucket,
} from "@/lib/object-storage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

export function BucketObjects({
  base,
  bucket,
  canWrite,
  canDelete,
  statusAction,
  back,
  notify,
}: {
  base: string;
  bucket: WorkerBucket;
  canWrite: boolean;
  canDelete: boolean;
  statusAction?: (transferring: boolean) => ReactNode;
  back: () => void;
  notify: (message: string) => void;
}) {
  const endpoint = base + "/buckets/" + encodeURIComponent(bucket.name) + "/objects";
  const [prefix, setPrefix] = useState(
    () => new URLSearchParams(location.search).get("prefix") || "",
  );
  const [path, setPath] = useState(prefix);
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const [page, setPage] = useState<ObjectPage | null>(null);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [detail, setDetail] = useState<BucketObject | null>(null);
  const [remove, setRemove] = useState<BucketObject | null>(null);
  const [busy, setBusy] = useState(false);
  const [transfer, setTransfer] = useState("");
  const [percent, setPercent] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const activeUpload = useRef<AbortController | null>(null);
  const detailRequest = useRef<AbortController | null>(null);

  useEffect(
    () => () => {
      activeUpload.current?.abort();
      detailRequest.current?.abort();
    },
    [],
  );

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    setPage(null);
    const query = new URLSearchParams({ prefix, cursor, limit: "100" });
    void api<ObjectPage>(endpoint + "?" + query, "GET", undefined, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) {
          setPage(value);
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(cause));
        }
      });
    return () => controller.abort();
  }, [endpoint, prefix, cursor, revision]);

  function navigate(value: string) {
    setPrefix(value);
    setPath(value);
    setCursors([""]);
    const url = new URL(location.href);
    if (value) {
      url.searchParams.set("prefix", value);
    } else {
      url.searchParams.delete("prefix");
    }
    history.replaceState(null, "", url);
  }

  async function showDetail(object: BucketObject) {
    detailRequest.current?.abort();
    const controller = new AbortController();
    detailRequest.current = controller;
    try {
      const result = await api<BucketObject>(
        endpoint + "/head?" + new URLSearchParams({ key: object.key }),
        "GET",
        undefined,
        controller.signal,
      );
      if (!controller.signal.aborted) {
        setDetail(result);
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        notify(errorMessage(cause));
      }
    }
  }

  async function upload(files: File[]) {
    if (!canWrite || !files.length || activeUpload.current) {
      return;
    }
    const controller = new AbortController();
    activeUpload.current = controller;
    try {
      for (const file of files) {
        setTransfer(file.name);
        setPercent(0);
        const access = await api<ObjectAccess>(
          endpoint + "/access",
          "POST",
          {
            operation: "upload",
            key: prefix + file.name,
            size: file.size,
            http_metadata: { contentType: file.type || "application/octet-stream" },
          },
          controller.signal,
        );
        await uploadObject(access, file, controller.signal, setPercent);
      }
      notify(t("上传完成。"));
    } catch (cause) {
      if (!controller.signal.aborted) {
        notify(errorMessage(cause));
      }
    } finally {
      activeUpload.current = null;
      setTransfer("");
      setCursors([""]);
      setRevision((value) => value + 1);
      if (input.current) {
        input.current.value = "";
      }
    }
  }

  async function deleteObject() {
    if (!canDelete || !remove) {
      return;
    }
    setBusy(true);
    try {
      await api(endpoint + "?" + new URLSearchParams({ key: remove.key }), "DELETE");
      setRemove(null);
      setDetail(null);
      setCursors([""]);
      setRevision((value) => value + 1);
      notify(t("对象已删除。"));
    } catch (cause) {
      notify(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }

  async function copyDelivery(key: string) {
    try {
      const result = await api<ObjectAccess>(endpoint + "/access", "POST", {
        operation: "delivery",
        key,
      });
      await navigator.clipboard.writeText(result.url);
      notify(t("已复制短期 CDN 链接。"));
    } catch (cause) {
      notify(errorMessage(cause));
    }
  }

  const segments = prefix.split("/").filter(Boolean);
  return (
    <>
      <Button
        variant="ghost"
        onClick={back}
      >
        <ArrowLeft size={16} />
        {t("返回桶列表")}
      </Button>
      <PageHeader
        title={bucket.name}
        description={t("上传下载直连存储。引用文件时保存桶和对象 key，短期链接到期后需重新获取。")}
        action={
          <div className="flex flex-wrap items-center gap-2">
            {statusAction?.(Boolean(transfer))}
            {canWrite && (
              <Button
                disabled={Boolean(transfer)}
                onClick={() => input.current?.click()}
              >
                <Upload size={16} />
                {t("上传文件")}
              </Button>
            )}
          </div>
        }
      />
      <p className="mb-4 text-xs text-muted-foreground">
        {t("创建时间")} ·{" "}
        <time dateTime={bucket.created_at}>{new Date(bucket.created_at).toLocaleString()}</time>
      </p>
      {canWrite ? (
        <input
          ref={input}
          type="file"
          multiple
          className="sr-only"
          aria-label={t("上传文件")}
          onChange={(event) => void upload(Array.from(event.target.files || []))}
        />
      ) : null}
      {canWrite ? (
        <p className="mb-4 text-xs text-muted-foreground">
          {t("同名对象将被覆盖；上传会保留 Worker 读取对象所需的元数据。")}
        </p>
      ) : null}
      <div
        className="mb-4 flex flex-wrap items-center gap-1"
        aria-label={t("对象目录")}
      >
        <Button
          variant="ghost"
          size="sm"
          onClick={() => navigate("")}
        >
          {t("根目录")}
        </Button>
        {segments.map((segment, index) => (
          <Button
            key={index}
            variant="ghost"
            size="sm"
            onClick={() => navigate(segments.slice(0, index + 1).join("/") + "/")}
          >
            / {segment}
          </Button>
        ))}
      </div>
      <form
        className="mb-4 flex gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          navigate(path);
        }}
      >
        <Input
          aria-label={t("路径前缀")}
          placeholder={t("路径前缀")}
          value={path}
          onChange={(event) => setPath(event.target.value)}
        />
        <Button
          variant="outline"
          type="submit"
        >
          {t("打开目录")}
        </Button>
        <Button
          variant="outline"
          type="button"
          aria-label={t("刷新")}
          onClick={() => setRevision((value) => value + 1)}
        >
          <RefreshCw size={16} />
        </Button>
      </form>
      {transfer ? (
        <div
          role="status"
          className="mb-4 flex items-center gap-3 rounded border p-3 text-sm"
        >
          <span className="min-w-0 flex-1 break-all">
            {transfer} — {percent}%
          </span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => activeUpload.current?.abort()}
          >
            {t("取消上传")}
          </Button>
        </div>
      ) : null}
      {error ? (
        <p
          role="alert"
          className="mb-4 text-sm text-destructive"
        >
          {error}
        </p>
      ) : !page ? (
        <Loading />
      ) : !page.objects.length && !page.delimited_prefixes.length ? (
        <Empty
          title={t("目录为空")}
          description={t("上传文件，或打开其他路径前缀。")}
        />
      ) : (
        <Card className="py-0 shadow-none">
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("对象名称")}</TableHead>
                  <TableHead className="hidden sm:table-cell">{t("大小")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("修改时间")}</TableHead>
                  <TableHead>{t("操作")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {page.delimited_prefixes.map((directory) => (
                  <TableRow key={"dir:" + directory}>
                    <TableCell colSpan={4}>
                      <Button
                        variant="ghost"
                        className="max-w-full justify-start"
                        onClick={() => navigate(directory)}
                      >
                        <Folder size={16} />
                        <span className="truncate">{directory.slice(prefix.length)}</span>
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
                {page.objects.map((object) => (
                  <TableRow key={object.key}>
                    <TableCell>
                      <Button
                        variant="ghost"
                        className="max-w-full justify-start"
                        onClick={() => void showDetail(object)}
                      >
                        <File size={16} />
                        <span className="truncate">
                          {object.key.slice(prefix.length) || object.key}
                        </span>
                      </Button>
                    </TableCell>
                    <TableCell className="hidden whitespace-nowrap sm:table-cell">
                      {object.size.toLocaleString()} B
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {new Date(object.uploaded).toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          asChild
                        >
                          <a
                            href={
                              endpoint + "/download?" + new URLSearchParams({ key: object.key })
                            }
                            referrerPolicy="no-referrer"
                          >
                            <Download size={14} />
                            {t("下载")}
                          </a>
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={!canDelete}
                          onClick={() => setRemove(object)}
                        >
                          {t("删除")}
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
      <div className="mt-4 flex justify-end gap-2">
        <Button
          variant="outline"
          disabled={cursors.length <= 1 || !page}
          onClick={() => setCursors((value) => value.slice(0, -1))}
        >
          {t("上一页")}
        </Button>
        <Button
          variant="outline"
          disabled={!page?.cursor}
          onClick={() => page?.cursor && setCursors((value) => [...value, page.cursor!])}
        >
          {t("下一页")}
        </Button>
      </div>
      <Dialog
        open={Boolean(detail)}
        onOpenChange={(open) => !open && setDetail(null)}
      >
        <DialogContent className="max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("对象详情")}</DialogTitle>
            <DialogDescription className="break-all">{detail?.key}</DialogDescription>
          </DialogHeader>
          {detail ? (
            <div className="grid gap-4 text-sm">
              <Field label={t("对象引用")}>
                <code className="break-all">
                  {JSON.stringify({ bucket: bucket.name, key: detail.key })}
                </code>
              </Field>
              <Field label={t("大小")}>
                <span>{detail.size.toLocaleString()} B</span>
              </Field>
              <Field label="ETag">
                <code className="break-all">{detail.etag}</code>
              </Field>
              <Field label={t("Worker 读取元数据")}>
                <span>
                  {detail.r2_compatible
                    ? t("兼容")
                    : t("缺少读取元数据，Worker 无法通过对象存储绑定读取此对象。")}
                </span>
              </Field>
              <Field label={t("HTTP 元数据")}>
                <pre className="overflow-x-auto whitespace-pre-wrap break-all">
                  {JSON.stringify(detail.http_metadata || {}, null, 2)}
                </pre>
              </Field>
              <Field label={t("自定义元数据")}>
                <pre className="overflow-x-auto whitespace-pre-wrap break-all">
                  {JSON.stringify(detail.custom_metadata || {}, null, 2)}
                </pre>
              </Field>
              {page?.delivery_available ? (
                <Button
                  variant="outline"
                  onClick={() => void copyDelivery(detail.key)}
                >
                  {t("复制短期 CDN 链接")}
                </Button>
              ) : null}
            </div>
          ) : null}
        </DialogContent>
      </Dialog>
      <Dialog
        open={Boolean(remove)}
        onOpenChange={(open) => !busy && !open && setRemove(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("删除对象")}</DialogTitle>
            <DialogDescription>
              {t("删除会影响所有使用此对象的 Worker，且无法撤销。")}
            </DialogDescription>
          </DialogHeader>
          <p className="break-all font-mono text-sm">{remove?.key}</p>
          <Button
            variant="destructive"
            disabled={busy}
            onClick={() => void deleteObject()}
          >
            {t("确认删除")}
          </Button>
        </DialogContent>
      </Dialog>
    </>
  );
}
