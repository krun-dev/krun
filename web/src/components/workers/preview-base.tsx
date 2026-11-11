import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import type { Environment } from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { R2BindingsEditor } from "./r2-bindings";
import { HyperdriveBindingsEditor } from "./hyperdrive-bindings";
import { bindingRows, bindingInput } from "@/lib/object-storage";
import { variableRows, variableInput } from "@/lib/worker-variables";
import { VariablesEditor, VersionMetadataField } from "./variables";
import { SecretsEditor } from "./secrets";
import { hyperdriveInput, hyperdriveRows } from "@/lib/worker-config-draft";
import { useConfigDraft } from "@/lib/use-config-draft";

export function PreviewBasePanel({
  base,
  organizationBase,
  notify,
}: {
  base: string;
  organizationBase: string;
  notify: (s: string) => void;
}) {
  const [config, setConfig] = useState<Environment>();
  const [message, setMessage] = useState("");
  const report = useCallback(
    (value: string) => {
      setMessage(value);
      notify(value);
    },
    [notify],
  );
  const draft = useConfigDraft({
    variables: {},
    r2_buckets: {},
    hyperdrive_bindings: {},
    version_metadata: "",
    secret_names: [],
  });
  const {
    variables,
    setVariables,
    versionMetadata,
    setVersionMetadata,
    r2Bindings,
    setR2Bindings,
    hyperdriveBindings,
    setHyperdriveBindings,
  } = draft;
  const [busy, setBusy] = useState(false);

  function reset(value: Environment) {
    setConfig(value);
    draft.reset(value);
  }

  useEffect(() => {
    const controller = new AbortController();
    setConfig(undefined);
    void api<Environment>(base + "/previews-base", "GET", undefined, controller.signal)
      .then(reset)
      .catch((error) => {
        if (!controller.signal.aborted) report(errorMessage(error));
      });
    return () => controller.abort();
  }, [base, report]);

  async function reload() {
    setBusy(true);
    try {
      reset(await api<Environment>(base + "/previews-base"));
    } catch (error) {
      report(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  async function save(section: "variables" | "r2" | "hyperdrive") {
    if (!config) return;
    setBusy(true);
    try {
      const input =
        section === "variables"
          ? { variables: variableInput(variables), version_metadata: versionMetadata }
          : section === "r2"
            ? {
                r2_buckets: bindingInput(
                  r2Bindings,
                  config.variables,
                  config.secret_names || [],
                  config.version_metadata || "",
                ),
              }
            : {
                hyperdrive_bindings: hyperdriveInput(hyperdriveBindings),
              };
      const result = await api<Environment>(base + "/previews-base", "PATCH", {
        ...input,
        expected_revision: config.configuration_revision,
      });
      setConfig(result);
      if (section === "variables") {
        setVariables(variableRows(result.variables));
        setVersionMetadata(result.version_metadata || "");
      } else if (section === "r2") {
        setR2Bindings(bindingRows(result.r2_buckets));
      } else {
        setHyperdriveBindings(hyperdriveRows(result.hyperdrive_bindings));
      }
      report(t("新建预览默认配置已保存，仅影响之后新建的预览。"));
    } catch (error) {
      report(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  async function changeSecret(name: string, value: string | null) {
    if (!config) return false;
    setBusy(true);
    try {
      await api(base + "/previews-base/secrets", "PATCH", {
        secrets: { [name]: value },
        expected_revision: config.deployment_revision,
        idempotency_key: crypto.randomUUID(),
      });
      setConfig(await api<Environment>(base + "/previews-base"));
      report(t("新建预览默认配置已保存，仅影响之后新建的预览。"));
      return true;
    } catch (error) {
      report(errorMessage(error));
      return false;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="grid gap-5 rounded-lg border p-5">
      {message ? (
        <p
          role="status"
          aria-live="polite"
          className="rounded-md border p-3 text-sm"
        >
          {message}
        </p>
      ) : null}
      <div>
        <h3 className="font-medium">{t("新建预览默认配置")}</h3>
        <p className="mt-2 text-sm text-muted-foreground">
          {t(
            "新建 Preview 时，复制这里的环境变量、Secrets 和对象存储绑定。各区域独立保存；已有 Preview 和生产环境不会随之改变。",
          )}
        </p>
      </div>
      <section
        aria-label={t("环境变量")}
        className="grid gap-3 border-t pt-4"
      >
        <h3 className="text-sm font-medium">{t("环境变量")}</h3>
        <VariablesEditor
          rows={variables}
          onChange={setVariables}
          disabled={busy || !config}
        />
        <VersionMetadataField
          value={versionMetadata}
          onChange={setVersionMetadata}
          disabled={busy || !config}
        />
        <div>
          <Button
            disabled={busy || !config}
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
        disabled={busy || !config}
        previewBase
      >
        <div>
          <Button
            disabled={busy || !config}
            onClick={() => void save("r2")}
          >
            {t("保存对象存储绑定")}
          </Button>
        </div>
      </R2BindingsEditor>
      <HyperdriveBindingsEditor
        organizationBase={organizationBase}
        rows={hyperdriveBindings}
        onChange={setHyperdriveBindings}
        disabled={busy || !config}
        onSave={() => void save("hyperdrive")}
      />
      <SecretsEditor
        names={config?.secret_names || []}
        disabled={busy || !config}
        previewBase
        onSave={changeSecret}
        onDelete={(name) => changeSecret(name, null)}
      />
      <div className="border-t pt-4">
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => void reload()}
        >
          {t("加载最新配置")}
        </Button>
        <p className="mt-2 text-xs text-muted-foreground">
          {t("重新加载会丢弃此页尚未保存的修改。")}
        </p>
      </div>
    </div>
  );
}
