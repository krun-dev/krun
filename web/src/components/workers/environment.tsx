import { useState } from "react";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import { shortID, type Environment, type WorkerData, type WorkerDomain } from "@/lib/workers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardContent } from "@/components/ui/card";
import { Field, SelectField } from "@/components/common";
import { workerURL } from "./shared";

export function EnvironmentSettings({
  base,
  env,
  active,
  data,
  domains,
  reload,
  notify,
}: {
  base: string;
  env: Environment;
  active: boolean;
  data: WorkerData;
  domains: WorkerDomain[];
  reload: () => void;
  notify: (s: string) => void;
}) {
  const [variables, setVariables] = useState(
    JSON.stringify(env.variables, null, 2),
  );
  const [revision, setRevision] = useState(env.configuration_revision);
  const [secretName, setSecretName] = useState("");
  const [secretValue, setSecretValue] = useState("");
  const [busy, setBusy] = useState(false);
  const domainLabel = (id: string) =>
    domains.find((d) => d.id === id)?.label ||
    domains.find((d) => d.id === id)?.domain ||
    id;
  const current = env.release_id
    ? data.releases[env.release_id]
    : undefined;
  const stale = revision !== env.configuration_revision;
  async function save(input: unknown) {
    setBusy(true);
    try {
      const result = await api<Environment>(
        base + "/environments/" + env.name,
        "PATCH",
        input,
      );
      setRevision(result.configuration_revision);
      reload();
      notify(t("环境配置已保存，下次发布时生效。"));
    } catch (e) {
      notify(errorMessage(e));
      reload();
    } finally {
      setBusy(false);
    }
  }
  async function changeSecret(
    method: "PUT" | "DELETE",
    name: string,
    value?: string,
  ) {
    setBusy(true);
    try {
      await api(
        base +
          "/environments/" +
          env.name +
          "/secrets/" +
          encodeURIComponent(name),
        method,
        {
          ...(method === "PUT" ? { value } : {}),
          expected_revision: env.release_revision,
          idempotency_key: crypto.randomUUID(),
        },
      );
      setSecretName("");
      setSecretValue("");
      reload();
      notify(
        method === "PUT"
          ? t("Secret 已保存，正在自动发布新版本。")
          : t("Secret 已删除，正在自动发布新版本。"),
      );
    } catch (e) {
      notify(errorMessage(e));
      reload();
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card className="py-0 shadow-none">
      <CardContent className="grid gap-5 p-5">
        <div className="flex items-center justify-between">
          <h3 className="font-medium">
            {env.name} · {t("环境配置")}
          </h3>
        </div>
        <div className="grid gap-2">
          <p className="text-sm font-medium">{t("访问地址")}</p>
          {env.hosts.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              {t("尚未选择域名，请到“路由与域名”配置。")}
            </p>
          ) : null}
          {env.hosts.map((host, i) => (
            <a
              key={host}
              href={workerURL(host)}
              target="_blank"
              rel="noreferrer"
              className="break-all text-xs text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground"
            >
              {domainLabel(env.domains[i])} · {host}
            </a>
          ))}
          <p className="text-xs text-muted-foreground">
            {t("域名由服务的“路由与域名”页统一选择，此处只展示当前环境的地址。")}
          </p>
        </div>
        <Field
          label={t("下一次发布使用的变量")}
          hint={t(
            "普通变量以 JSON 对象填写，保存配置不会修改正在运行的版本。请勿填入 Secret。",
          )}
        >
          <Textarea
            aria-label={t("环境变量 JSON")}
            className="min-h-40 font-mono text-xs"
            value={variables}
            onChange={(e) => setVariables(e.target.value)}
          />
        </Field>
        {stale ? (
          <p role="alert" className="text-sm text-destructive">
            {t("配置已被更新，请先加载最新配置。")}
          </p>
        ) : null}
        <div className="flex gap-2">
          <Button
            disabled={busy || active || stale}
            onClick={() => {
              try {
                const parsed: unknown = JSON.parse(variables);
                if (
                  !parsed ||
                  typeof parsed !== "object" ||
                  Array.isArray(parsed) ||
                  Object.values(parsed).some((v) => typeof v !== "string")
                )
                  throw new Error(t("变量必须是字符串键值对象。"));
                void save({ expected_revision: revision, variables: parsed });
              } catch (e) {
                notify(errorMessage(e));
              }
            }}
          >
            {t("保存配置")}
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              setVariables(JSON.stringify(env.variables, null, 2));
              setRevision(env.configuration_revision);
            }}
          >
            {t("加载最新配置")}
          </Button>
        </div>
        <div className="grid gap-3 border-t pt-4">
          <div>
            <p className="text-sm font-medium">{t("Secrets")}</p>
            <p className="mt-1 text-xs text-muted-foreground">
              {t(
                "Secret 值加密保存且不可读取。新增、轮换或删除会复用当前 Artifact 自动发布，不会重新构建代码。",
              )}
            </p>
          </div>
          {(env.secret_names || []).length > 0 ? (
            <div className="grid gap-2">
              {(env.secret_names || []).map((name) => (
                <div
                  key={name}
                  className="flex items-center justify-between gap-3 rounded-md border px-3 py-2"
                >
                  <code className="text-xs">{name}</code>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy || active}
                    onClick={() => {
                      if (window.confirm(t("确定删除这个 Secret 并立即发布吗？")))
                        void changeSecret("DELETE", name);
                    }}
                  >
                    {t("删除")}
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">
              {t("当前环境还没有 Secret。")}
            </p>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("Secret 名称")}>
              <Input
                autoComplete="off"
                placeholder="API_TOKEN"
                value={secretName}
                onChange={(e) => setSecretName(e.target.value.toUpperCase())}
              />
            </Field>
            <Field label={t("Secret 值")}>
              <Input
                type="password"
                autoComplete="new-password"
                value={secretValue}
                onChange={(e) => setSecretValue(e.target.value)}
              />
            </Field>
          </div>
          <div>
            <Button
              disabled={
                busy ||
                active ||
                !env.release_id ||
                !/^[A-Z][A-Z0-9_]{0,63}$/.test(secretName) ||
                secretName.startsWith("KRUN_") ||
                secretValue.length === 0
              }
              onClick={() =>
                void changeSecret("PUT", secretName, secretValue)
              }
            >
              {t("保存并发布 Secret")}
            </Button>
          </div>
        </div>
        <div className="border-t pt-4">
          <p className="mb-2 text-sm text-muted-foreground">
            {t("当前 Release 使用的变量")} · {shortID(current?.id)}
          </p>
          <pre className="rounded-md bg-secondary p-3 text-xs">
            {JSON.stringify(current?.variables || {}, null, 2)}
          </pre>
        </div>
      </CardContent>
    </Card>
  );
}

export function Invocation({ base, env }: { base: string; env: Environment }) {
  const [path, setPath] = useState("/");
  const [method, setMethod] = useState("GET");
  const [body, setBody] = useState("{}");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<{
    status: number;
    body: string;
    headers: Record<string, string>;
    truncated: boolean;
  } | null>(null);
  let display = result?.body || "";
  try {
    display = JSON.stringify(JSON.parse(display), null, 2);
  } catch {
    /* Non-JSON responses stay escaped text. */
  }
  return (
    <Card className="py-0 shadow-none">
      <CardContent className="grid gap-5 p-5">
        <div>
          <h3 className="font-medium">
            {env.name} · {t("调用已发布 Worker")}
          </h3>
          <div className="mt-1 grid gap-0.5">
            {(env.hosts || []).map((host) => (
              <a
                key={host}
                href={workerURL(host)}
                target="_blank"
                rel="noreferrer"
                className="break-all text-xs text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground"
              >
                {host}
              </a>
            ))}
          </div>
        </div>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError("");
            try {
              setResult(
                await api(
                  base + "/environments/" + env.name + "/invoke",
                  "POST",
                  {
                    path,
                    method,
                    body: method === "GET" || method === "HEAD" ? "" : body,
                  },
                ),
              );
            } catch (e) {
              setError(errorMessage(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          <div className="flex gap-3">
            <div className="w-28">
              <SelectField
                label={t("请求方法")}
                value={method}
                onChange={setMethod}
              >
                {[
                  "GET",
                  "POST",
                  "PUT",
                  "PATCH",
                  "DELETE",
                  "HEAD",
                  "OPTIONS",
                ].map((m) => (
                  <option key={m}>{m}</option>
                ))}
              </SelectField>
            </div>
            <Input
              aria-label={t("请求路径")}
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/orders?id=1"
              required
            />
          </div>
          {method !== "GET" && method !== "HEAD" ? (
            <Field label={t("请求体")}>
              <Textarea
                value={body}
                onChange={(e) => setBody(e.target.value)}
                className="font-mono text-xs"
              />
            </Field>
          ) : null}
          <Button
            className="w-fit"
            disabled={busy || !env.enabled || !env.release_id}
            type="submit"
          >
            {busy ? t("调用中…") : t("发送请求")}
          </Button>
        </form>
        {!env.release_id ? (
          <p className="text-sm text-muted-foreground">
            {t("先发布到此环境，再发送请求。")}
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {result ? (
          <div className="grid gap-3">
            <Badge variant="outline" className="w-fit">
              HTTP {result.status}
            </Badge>
            <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded-md bg-secondary p-4 text-xs">
              {display}
            </pre>
            <details className="text-xs text-muted-foreground">
              <summary>{t("响应信息")}</summary>
              <pre className="mt-2 whitespace-pre-wrap break-all">
                {JSON.stringify(result.headers, null, 2)}
              </pre>
            </details>
            {result.truncated ? <p>{t("响应超过 64 KiB，已截断。")}</p> : null}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
