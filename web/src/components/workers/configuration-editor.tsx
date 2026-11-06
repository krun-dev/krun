import { useRef, useState } from "react";
import { api } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import type { Environment, Version } from "@/lib/workers";
import type { WorkerConfiguration } from "@/lib/worker-configurations";
import { variableRows, variableInput } from "@/lib/worker-variables";
import { bindingRows, bindingInput } from "@/lib/object-storage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Field } from "@/components/common";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { VariablesEditor, VersionMetadataField } from "./variables";
import { R2BindingsEditor } from "./r2-bindings";
import { SecretsEditor } from "./secrets";

export type ConfigurationSource = { configuration?: WorkerConfiguration; version?: Version };

export function ConfigurationEditor({
  base,
  organizationBase,
  env,
  source,
  close,
  saved,
}: {
  base: string;
  organizationBase: string;
  env: Environment;
  source: ConfigurationSource;
  close: () => void;
  saved: (configuration: WorkerConfiguration) => void;
}) {
  const original = source.configuration || source.version || env;
  const [name, setName] = useState(
    source.configuration ? source.configuration.name + " · 调整" : "",
  );
  const [message, setMessage] = useState("");
  const [variables, setVariables] = useState(() => variableRows(original.variables));
  const [bindings, setBindings] = useState(() => bindingRows(original.r2_buckets));
  const [metadata, setMetadata] = useState(original.version_metadata || "");
  const [secrets, setSecrets] = useState<Record<string, string | null>>({});
  const [revision] = useState(env.configuration_revision);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef({ fingerprint: "", key: "" });
  const submitting = useRef(false);
  const stale = revision !== env.configuration_revision;
  const secretNames = [...new Set([...(original.secret_names || []), ...Object.keys(secrets)])]
    .filter((name) => secrets[name] !== null)
    .sort();

  async function save() {
    if (submitting.current || stale) return;
    setError("");
    try {
      const values = variableInput(variables);
      const payload = {
        name: name.trim(),
        message: message.trim(),
        variables: values,
        r2_buckets: bindingInput(bindings, values, secretNames, metadata),
        version_metadata: metadata,
        secrets,
        expected_configuration_revision: revision,
        ...(source.configuration ? { source_configuration_id: source.configuration.id } : {}),
        ...(source.version ? { source_version_id: source.version.id } : {}),
      };
      if (
        !payload.name ||
        new TextEncoder().encode(payload.name).length > 100 ||
        new TextEncoder().encode(payload.message).length > 1000
      ) {
        throw new Error(t("配置名称必填且最多 100 字节，说明最多 1000 字节。"));
      }
      const fingerprint = JSON.stringify(payload);
      if (request.current.fingerprint !== fingerprint)
        request.current = { fingerprint, key: crypto.randomUUID() };
      submitting.current = true;
      setBusy(true);
      const configuration = await api<WorkerConfiguration>(
        base + "/environments/" + env.name + "/configurations",
        "POST",
        { ...payload, idempotency_key: request.current.key },
      );
      saved(configuration);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  async function changeSecret(name: string, value: string | null) {
    setSecrets((previous) => ({ ...previous, [name]: value }));
    return true;
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) close();
      }}
    >
      <DialogContent
        className="max-h-[85dvh] overflow-y-auto sm:max-w-2xl"
        showCloseButton={!busy}
      >
        <DialogHeader>
          <DialogTitle>{t("保存新配置版本")}</DialogTitle>
          <DialogDescription>
            {t("配置独立保存，不会改变当前运行。发布成功后才切换生效。")}
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-5">
          <p className="break-all text-sm">
            {t("目标环境")} · {env.name}
          </p>
          <Field label={t("配置名称")}>
            <Input
              aria-label={t("配置名称")}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("例如：XXX 测试、支付联调、正式基线")}
              maxLength={100}
              disabled={busy}
            />
          </Field>
          <Field label={t("配置说明")}>
            <Textarea
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              maxLength={1000}
              disabled={busy}
            />
          </Field>
          <section
            aria-label={t("配置版本变量")}
            className="grid gap-3 border-t pt-4"
          >
            <h3 className="text-sm font-medium">{t("环境变量")}</h3>
            <VariablesEditor
              rows={variables}
              onChange={setVariables}
              disabled={busy}
            />
            <VersionMetadataField
              value={metadata}
              onChange={setMetadata}
              disabled={busy}
            />
          </section>
          <R2BindingsEditor
            organizationBase={organizationBase}
            rows={bindings}
            onChange={setBindings}
            disabled={busy}
          />
          <SecretsEditor
            pendingNames={Object.keys(secrets)}
            saveLabel={t("加入此配置")}
            names={secretNames}
            disabled={busy}
            effectDescription={t(
              "Secret 变更暂存在本次编辑中，保存配置后加密入库，发布成功后生效。",
            )}
            onSave={changeSecret}
            onDelete={(name) => changeSecret(name, null)}
          />
          {stale ? (
            <p
              role="alert"
              className="text-sm text-destructive"
            >
              {t("环境配置已改变，请关闭后重新创建并核对配置。")}
            </p>
          ) : null}
          {error ? (
            <p
              role="alert"
              className="text-sm text-destructive"
            >
              {error}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={close}
            >
              {t("取消")}
            </Button>
            <Button
              disabled={busy || stale || !name.trim()}
              onClick={() => void save()}
            >
              {busy ? t("保存中…") : t("保存新版本")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
