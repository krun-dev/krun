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
import { R2BindingsEditor } from "./r2-bindings";
import { bindingRows, bindingInput } from "@/lib/object-storage";
import { variableRows, variableInput } from "@/lib/worker-variables";
import { VariablesEditor, VersionMetadataField } from "./variables";
import { SecretsEditor } from "./secrets";

export function EnvironmentSettings({
  base,
  organizationBase,
  env,
  active,
  data,
  domains,
  reload,
  notify,
}: {
  base: string;
  organizationBase: string;
  env: Environment;
  active: boolean;
  data: WorkerData;
  domains: WorkerDomain[];
  reload: () => void;
  notify: (s: string) => void;
}) {
  const [variables, setVariables] = useState(() => variableRows(env.variables));
  const [versionMetadata, setVersionMetadata] = useState(env.version_metadata || "");
  const [r2Bindings, setR2Bindings] = useState(() => bindingRows(env.r2_buckets));
  const [revision, setRevision] = useState(env.configuration_revision);
  const [busy, setBusy] = useState(false);
  const domainLabel = (id: string) =>
    domains.find((d) => d.id === id)?.label || domains.find((d) => d.id === id)?.domain || id;
  const current = env.version_id ? data.versions[env.version_id] : undefined;
  const stale = revision !== env.configuration_revision;
  async function save(section: "variables" | "r2") {
    setBusy(true);
    try {
      const input =
        section === "variables"
          ? { variables: variableInput(variables), version_metadata: versionMetadata }
          : {
              r2_buckets: bindingInput(
                r2Bindings,
                env.variables,
                env.secret_names || [],
                env.version_metadata || "",
              ),
            };
      const result = await api<Environment>(base + "/environments/" + env.name, "PATCH", {
        ...input,
        expected_revision: revision,
      });
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
  async function changeSecret(method: "PUT" | "DELETE", name: string, value?: string) {
    setBusy(true);
    try {
      await api(
        base + "/environments/" + env.name + "/secrets/" + encodeURIComponent(name),
        method,
        {
          ...(method === "PUT" ? { value } : {}),
          expected_revision: env.deployment_revision,
          idempotency_key: crypto.randomUUID(),
        },
      );
      reload();
      notify(
        !env.version_id
          ? t("Secret 配置已保存，首次部署时生效。")
          : method === "PUT"
            ? t("Secret 已保存，正在自动发布新版本。")
            : t("Secret 已删除，正在自动发布新版本。"),
      );
      return true;
    } catch (e) {
      notify(errorMessage(e));
      reload();
      return false;
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
            {t("域名由Worker的“路由与域名”页统一选择，此处只展示当前环境的地址。")}
          </p>
        </div>
        <section
          aria-label={t("环境变量")}
          className="grid gap-3 border-t pt-4"
        >
          <h3 className="text-sm font-medium">{t("环境变量")}</h3>
          <p className="text-xs text-muted-foreground">
            {t("保存后在下一次发布时生效，不会修改正在运行的版本。")}
          </p>
          <VariablesEditor
            rows={variables}
            onChange={setVariables}
            disabled={busy || active || stale}
          />
          <VersionMetadataField
            value={versionMetadata}
            onChange={setVersionMetadata}
            disabled={busy || active || stale}
          />
          <div>
            <Button
              disabled={busy || active || stale}
              onClick={() => void save("variables")}
            >
              {t("保存环境变量")}
            </Button>
          </div>
        </section>
        <R2BindingsEditor
          organizationBase={organizationBase}
          rows={r2Bindings}
          onChange={setR2Bindings}
          disabled={busy || active || stale}
        >
          <div>
            <Button
              disabled={busy || active || stale}
              onClick={() => void save("r2")}
            >
              {t("保存对象存储绑定")}
            </Button>
          </div>
        </R2BindingsEditor>
        {stale ? (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {t("配置已被更新，请先加载最新配置。")}
          </p>
        ) : null}
        <div className="flex gap-2">
          <Button
            variant="outline"
            disabled={busy || active}
            onClick={() => {
              setVariables(variableRows(env.variables));
              setVersionMetadata(env.version_metadata || "");
              setR2Bindings(bindingRows(env.r2_buckets));
              setRevision(env.configuration_revision);
            }}
          >
            {t("加载最新配置")}
          </Button>
        </div>
        <SecretsEditor
          names={env.secret_names || []}
          disabled={busy || active}
          published={Boolean(env.version_id)}
          onSave={(name, value) => changeSecret("PUT", name, value)}
          onDelete={(name) => changeSecret("DELETE", name)}
        />
        <div className="border-t pt-4">
          <p className="mb-2 text-sm text-muted-foreground">{t("当前版本使用的对象存储绑定")}</p>
          <pre className="mb-4 overflow-auto rounded-md bg-secondary p-3 text-xs">
            {JSON.stringify(
              Object.fromEntries(
                Object.entries(current?.r2_buckets || {}).map(([name, bucket]) => [
                  name,
                  bucket.bucket_name,
                ]),
              ),
              null,
              2,
            )}
          </pre>
          <p className="mb-2 text-sm text-muted-foreground">
            {t("当前 Version 使用的变量")} · {shortID(current?.id)}
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
                await api(base + "/environments/" + env.name + "/invoke", "POST", {
                  path,
                  method,
                  body: method === "GET" || method === "HEAD" ? "" : body,
                }),
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
                {["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].map((m) => (
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
            disabled={busy || !env.enabled || !env.version_id}
            type="submit"
          >
            {busy ? t("调用中…") : t("发送请求")}
          </Button>
        </form>
        {!env.version_id ? (
          <p className="text-sm text-muted-foreground">{t("先发布到此环境，再发送请求。")}</p>
        ) : null}
        {error ? (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {error}
          </p>
        ) : null}
        {result ? (
          <div className="grid gap-3">
            <Badge
              variant="outline"
              className="w-fit"
            >
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
