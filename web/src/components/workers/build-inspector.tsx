import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Check, Copy, Download, FileCode2, GitBranch, Package, Terminal } from "lucide-react";
import { t, errorMessage } from "@/lib/i18n";
import { pending, type Build } from "@/lib/workers";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Status } from "./shared";

export type BuildArtifact = {
  id: string;
  state: string;
  profile: string;
  archive_sha256: string;
  archive_size_bytes: number;
  compatibility_date: string;
  compatibility_flags?: string[];
};

function Detail({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="mb-1.5 text-xs text-muted-foreground">{label}</dt>
      <dd className="break-all text-sm">{children || "—"}</dd>
    </div>
  );
}

export function BuildInspector({
  info,
  onClose,
  notify,
}: {
  info: {
    build: Build;
    log: string;
    pollSeconds: number | null;
    artifact?: BuildArtifact;
    artifactError?: string;
  };
  onClose: () => void;
  notify: (message: string) => void;
}) {
  useTranslation();
  const [copiedLog, setCopiedLog] = useState<string | null>(null);
  const { build, log, artifact } = info;
  const copied = copiedLog === log;
  const source = build.repository_source;
  const lines = log ? log.replace(/\r\n/g, "\n").split("\n") : [];
  if (lines.at(-1) === "") lines.pop();
  const running = pending(build.state);

  async function copyLog() {
    try {
      await navigator.clipboard.writeText(log);
      setCopiedLog(log);
    } catch (error) {
      notify(errorMessage(error));
    }
  }

  function downloadLog() {
    const url = URL.createObjectURL(new Blob([log], { type: "text/plain;charset=utf-8" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `${build.id}.log`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[90dvh] gap-0 overflow-y-auto p-0 sm:max-w-6xl">
        <DialogHeader className="gap-4 border-b px-5 py-5 text-left sm:px-7 sm:py-6">
          <div className="flex items-center gap-3 pr-6">
            <span className="flex size-10 shrink-0 items-center justify-center rounded-xl border border-primary/15 bg-primary/5 text-primary">
              <Terminal size={20} />
            </span>
            <div className="min-w-0 flex-1">
              <DialogTitle>{t("日志与制品")}</DialogTitle>
              <DialogDescription className="mt-2 break-all font-mono text-xs">
                {build.id}
              </DialogDescription>
            </div>
            <Status state={build.state} />
          </div>
          <dl className="grid gap-4 rounded-lg border bg-secondary/35 p-4 sm:grid-cols-3 sm:gap-6">
            <Detail label={t("代码源")}>
              <span className="flex items-start gap-2">
                {source ? (
                  <GitBranch
                    size={15}
                    className="mt-0.5 shrink-0 text-muted-foreground"
                  />
                ) : (
                  <FileCode2
                    size={15}
                    className="mt-0.5 shrink-0 text-muted-foreground"
                  />
                )}
                {source?.repository || t("源码上传")}
              </span>
              {source ? (
                <span className="mt-1 block font-mono text-xs text-muted-foreground">
                  {source.ref} · {source.commit?.slice(0, 12) || t("等待解析 Commit")}
                </span>
              ) : null}
            </Detail>
            <Detail label={t("入口文件")}>
              <code className="text-xs">{build.entrypoint || "—"}</code>
            </Detail>
            <Detail label={t("创建时间")}>
              <time dateTime={build.created_at}>{new Date(build.created_at).toLocaleString()}</time>
            </Detail>
          </dl>
        </DialogHeader>

        <div className="grid min-w-0 lg:grid-cols-[minmax(0,1fr)_300px]">
          <section
            className="min-w-0 p-5 sm:p-7 lg:border-r"
            aria-label={t("构建日志")}
          >
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <h3 className="flex items-center gap-2 text-sm font-medium">
                <Terminal
                  size={16}
                  className="text-muted-foreground"
                />
                {t("构建日志")}
              </h3>
              <div className="flex gap-1">
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={!log}
                  onClick={() => void copyLog()}
                >
                  {copied ? <Check /> : <Copy />}
                  {copied ? t("已复制") : t("复制日志")}
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={!log}
                  onClick={downloadLog}
                >
                  <Download />
                  {t("下载日志")}
                </Button>
              </div>
            </div>
            <div className="overflow-hidden rounded-lg border border-zinc-800 bg-[#151918] text-zinc-300">
              <div className="flex items-center justify-between gap-3 border-b border-white/10 px-4 py-2.5 font-mono text-[11px] text-zinc-400">
                <span>build.log</span>
                <span className="inline-flex items-center gap-1.5">
                  <span
                    className={
                      running
                        ? "size-1.5 animate-pulse rounded-full bg-amber-400"
                        : "size-1.5 rounded-full bg-zinc-500"
                    }
                  />
                  {info.pollSeconds ? t("自动更新中") : running ? t("等待日志") : t("日志已归档")}
                </span>
              </div>
              <div
                className="h-80 overflow-auto py-4 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary sm:h-96"
                tabIndex={0}
                aria-label={t("日志内容")}
              >
                {lines.length ? (
                  <pre className="w-max min-w-full font-mono text-xs leading-6">
                    {lines.map((line, index) => (
                      <div
                        key={index}
                        className="flex px-4 hover:bg-white/5"
                      >
                        <span
                          aria-hidden="true"
                          className="mr-5 w-7 shrink-0 select-none text-right text-zinc-600"
                        >
                          {index + 1}
                        </span>
                        <span className="pr-4">{line || " "}</span>
                      </div>
                    ))}
                  </pre>
                ) : (
                  <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-zinc-500">
                    <Terminal size={26} />
                    <p className="text-xs">
                      {running ? t("构建进行中，等待日志输出。") : t("暂无构建日志。")}
                    </p>
                  </div>
                )}
              </div>
              <div className="flex justify-between border-t border-white/10 px-4 py-2.5 text-[11px] text-zinc-500">
                <span>{t("{{count}} 行", { count: lines.length })}</span>
                <span>UTF-8</span>
              </div>
            </div>
            {build.error ? (
              <p
                role="alert"
                className="mt-3 break-words rounded-md border border-destructive/20 bg-destructive/5 p-3 text-xs text-destructive"
              >
                {build.error}
              </p>
            ) : null}
          </section>

          <aside className="min-w-0 border-t bg-secondary/15 p-5 sm:p-7 lg:border-t-0">
            <h3 className="mb-5 flex items-center gap-2 text-sm font-medium">
              <Package
                size={16}
                className="text-muted-foreground"
              />
              {t("构建制品")}
            </h3>
            {artifact ? (
              <>
                <div className="mb-5 flex items-center gap-2 rounded-lg border border-primary/15 bg-primary/5 px-3 py-2.5 text-xs text-primary">
                  <Package size={15} />
                  <span>{t("制品状态")}</span>
                  <span className="ml-auto font-mono">
                    {artifact.state === "finalized" ? t("已就绪") : artifact.state}
                  </span>
                </div>
                <dl className="grid gap-5">
                  <Detail label={t("制品 ID")}>
                    <code className="text-xs">{artifact.id}</code>
                  </Detail>
                  <div className="grid grid-cols-2 gap-4">
                    <Detail label={t("归档大小")}>
                      {artifact.archive_size_bytes < 1024
                        ? `${artifact.archive_size_bytes} B`
                        : `${(artifact.archive_size_bytes / 1024).toLocaleString(undefined, { maximumFractionDigits: 1 })} KB`}
                    </Detail>
                    <Detail label={t("兼容日期")}>{artifact.compatibility_date}</Detail>
                  </div>
                  <Detail label={t("运行配置")}>
                    <code className="text-xs">{artifact.profile}</code>
                  </Detail>
                  <Detail label="SHA-256">
                    <code className="text-[11px] leading-5 text-muted-foreground">
                      {artifact.archive_sha256}
                    </code>
                  </Detail>
                </dl>
              </>
            ) : (
              <div className="rounded-lg border border-dashed px-4 py-8 text-center">
                <Package
                  size={28}
                  className="mx-auto mb-3 text-muted-foreground/50"
                />
                <p className="text-sm text-muted-foreground">
                  {info.artifactError ? t("制品信息加载失败") : t("暂无构建制品")}
                </p>
                <p className="mt-2 text-xs leading-5 text-muted-foreground">
                  {info.artifactError ||
                    (running
                      ? t("构建成功后将在此展示。")
                      : build.error || t("此构建没有可用制品。"))}
                </p>
              </div>
            )}
          </aside>
        </div>
        <details className="border-t px-5 py-4 text-xs sm:px-7">
          <summary className="w-fit cursor-pointer text-muted-foreground transition-colors hover:text-foreground">
            {t("原始元数据")}
          </summary>
          <pre className="mt-3 max-h-56 overflow-auto rounded-md border bg-secondary/40 p-4 font-mono text-[11px] leading-5">
            {JSON.stringify({ build, artifact }, null, 2)}
          </pre>
        </details>
      </DialogContent>
    </Dialog>
  );
}
