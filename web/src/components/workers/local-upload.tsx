import { useEffect, useId, useRef, useState, type ChangeEvent, type DragEvent } from "react";
import { LoaderCircle, Rocket, Upload } from "lucide-react";
import { Field } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { APIError } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import {
  localProjectFromDrop,
  localProjectFromFiles,
  submitLocalBuild,
  type LocalBuild,
  type LocalProject,
} from "@/lib/local-build";
import { cn } from "@/lib/utils";
import { shortID, type Build, type Environment } from "@/lib/workers";
import { idempotency, Status } from "./shared";

type Attempt = {
  project: LocalProject;
  environment: string;
  root: string;
  key: string;
};

type Result = LocalBuild & { name: string; environment: string };

export function LocalUpload({
  base,
  worker,
  env,
  builds,
  canPublish,
  onQueued,
  onPublish,
}: {
  base: string;
  worker: string;
  env: Environment;
  builds: Build[];
  canPublish: (environment: string) => boolean;
  onQueued: () => void;
  onPublish: (build: Build, environment: string) => void;
}) {
  const titleID = useId();
  const directoryInput = useRef<HTMLInputElement>(null);
  const archiveInput = useRef<HTMLInputElement>(null);
  const request = useRef<AbortController | null>(null);
  const [dragging, setDragging] = useState(false);
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState<Attempt | null>(null);
  const [error, setError] = useState<{ message: string; rootRequired: boolean } | null>(null);
  const [root, setRoot] = useState("");
  const [result, setResult] = useState<Result | null>(null);
  const build = result && (builds.find((item) => item.id === result.build.id) ?? result.build);
  const disabled = busy || !env.enabled;
  const target = attempt?.environment ?? env.name;
  const showRoot = attempt && error && (error.rootRequired || attempt.root);

  useEffect(() => () => request.current?.abort(), []);

  async function upload(prepare: (signal: AbortSignal) => Attempt | Promise<Attempt>) {
    if (request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError(null);
    setAttempt(null);
    setResult(null);

    try {
      const next = await prepare(controller.signal);
      controller.signal.throwIfAborted();
      setAttempt(next);
      const accepted = await submitLocalBuild(
        `${base}/environments/${encodeURIComponent(next.environment)}/local-builds`,
        next.project,
        next.root,
        next.key,
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setAttempt(null);
      setResult({ ...accepted, name: next.project.name, environment: next.environment });
      onQueued();
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError({
          message: errorMessage(cause),
          rootRequired: cause instanceof APIError && cause.code === "LOCAL_PROJECT_ROOT_REQUIRED",
        });
      }
    } finally {
      request.current = null;
      if (!controller.signal.aborted) setBusy(false);
    }
  }

  function pick(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.currentTarget.files ?? []);
    event.currentTarget.value = "";
    if (disabled || files.length === 0) return;
    setRoot("");
    void upload(() => ({
      project: localProjectFromFiles(files),
      environment: env.name,
      root: "",
      key: idempotency(),
    }));
  }

  function drop(event: DragEvent<HTMLElement>) {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    setDragging(false);
    if (disabled) return;
    setRoot("");
    void upload(async (signal) => ({
      project: await localProjectFromDrop(event.dataTransfer, signal),
      environment: env.name,
      root: "",
      key: idempotency(),
    }));
  }

  function retry() {
    if (!attempt) return;
    const nextRoot = root.trim();
    void upload(() => ({
      ...attempt,
      root: nextRoot,
      key: nextRoot === attempt.root ? attempt.key : idempotency(),
    }));
  }

  return (
    <section
      aria-labelledby={titleID}
      className={cn(
        "grid gap-3 rounded-lg border border-dashed bg-card p-4",
        dragging && "border-primary bg-primary/5",
      )}
      onDragOver={(event) => {
        if (!event.dataTransfer.types.includes("Files")) return;
        event.preventDefault();
        event.dataTransfer.dropEffect = disabled ? "none" : "copy";
        if (!disabled) setDragging(true);
      }}
      onDragLeave={(event) => {
        if (
          !(event.relatedTarget instanceof Node) ||
          !event.currentTarget.contains(event.relatedTarget)
        ) {
          setDragging(false);
        }
      }}
      onDrop={drop}
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <Upload className="mt-0.5 size-5 shrink-0 text-primary" />
          <div className="grid min-w-0 gap-1">
            <h3
              id={titleID}
              className="text-sm font-medium"
            >
              {t("上传本地项目")}
            </h3>
            <p className="text-xs text-muted-foreground">
              {t("将项目文件夹或 ZIP 压缩包拖到这里。")}
            </p>
            <p className="break-all text-xs text-muted-foreground">
              {t("上传目标")} · {worker} / {target}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => directoryInput.current?.click()}
          >
            {t("选择文件夹")}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => archiveInput.current?.click()}
          >
            {t("选择 ZIP")}
          </Button>
        </div>
      </div>
      <input
        ref={(input) => {
          directoryInput.current = input;
          if (input) input.webkitdirectory = true;
        }}
        type="file"
        hidden
        onChange={pick}
      />
      <input
        ref={archiveInput}
        type="file"
        accept=".zip,application/zip"
        hidden
        onChange={pick}
      />
      {busy ? (
        <p
          role="status"
          className="flex items-center gap-2 text-sm text-muted-foreground"
        >
          <LoaderCircle className="size-4 animate-spin" />
          {t("正在准备并上传项目…")}
        </p>
      ) : null}
      {error ? (
        <div className="grid justify-items-start gap-2">
          <p
            role="alert"
            className="break-words text-sm text-destructive"
          >
            {error.message}
          </p>
          {showRoot ? (
            <div className="w-full max-w-md">
              <Field label={t("项目目录")}>
                <Input
                  value={root}
                  onChange={(event) => setRoot(event.target.value)}
                  placeholder="."
                />
              </Field>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("填写相对上传文件夹或 ZIP 内容的路径，使用 . 表示根目录。")}
              </p>
            </div>
          ) : null}
          {attempt ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={!!showRoot && !root.trim()}
              onClick={retry}
            >
              {t("重试上传")}
            </Button>
          ) : null}
        </div>
      ) : null}
      {result && build ? (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-3">
          <div
            role="status"
            className="grid min-w-0 gap-1 text-sm"
          >
            <div className="flex flex-wrap items-center gap-2">
              <strong className="break-all font-medium">{result.name}</strong>
              <span className="text-muted-foreground">
                {result.kind === "static" ? t("静态网站") : t("Worker 项目")}
              </span>
              <Status state={build.state} />
            </div>
            <p className="break-all text-xs text-muted-foreground">
              {worker} / {result.environment} · <code title={build.id}>{shortID(build.id)}</code>
            </p>
            {build.error ? <p className="text-xs text-destructive">{build.error}</p> : null}
          </div>
          {build.state === "succeeded" ? (
            <Button
              type="button"
              size="sm"
              disabled={!canPublish(result.environment)}
              onClick={() => onPublish(build, result.environment)}
            >
              <Rocket size={14} />
              {t("发布构建版本")}
            </Button>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
